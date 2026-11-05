# Deploying SAF

The runbook for Phase 9 of [`PLAN.md`](PLAN.md): one small VM on GCP running the SAF container,
HTTPS from Let's Encrypt, every database change streamed to a bucket by Litestream, photos copied
there hourly, and a firewall that only lets Brazil reach the app. Costs and the reasons behind each
choice are in `PLAN.md` §3 and §6.

Every command that creates or changes something in the cloud is marked ☁. Run them one at a time,
read the output, and stop at the first surprise.

**Already done locally** (not repeated here): the image builds for the VM (`make release`), CI runs
`make check` and the Trivy scan on every push, and the restore drill passed against a local
Litestream replica.

## 0. Before you start

Decisions that must be made first:

- **Where:** GCP `us-east1` unless UNILAB's encarregado requires the data in Brazil (open item 1).
  Then this runbook changes: Oracle Cloud São Paulo or a UNILAB server.
- **Account:** a GCP account owned by the pharmacy, with billing enabled. Expect about US$3.65 a
  month for the public IPv4 address; the rest is free tier.
- **Domain:** a free DuckDNS subdomain, e.g. `saf-farmacia.duckdns.org`.
- **Key custody:** who besides Thiago keeps a copy of the encryption key (open item 4).

On the laptop: this repo, Docker, and the gcloud CLI (`brew install --cask google-cloud-sdk`,
then `gcloud auth login`).

The names used below; change them once here:

```sh
export PROJECT=saf-farmacia-unilab REGION=us-east1 ZONE=us-east1-b
export BUCKET=saf-farmacia-backup  DOMAIN=saf-farmacia.duckdns.org
export SA=saf-vm@$PROJECT.iam.gserviceaccount.com
```

## 1. Project ☁

```sh
gcloud projects create $PROJECT --name="SAF Farmácia UNILAB"
gcloud billing accounts list
gcloud billing projects link $PROJECT --billing-account=<ACCOUNT_ID>
gcloud config set project $PROJECT
gcloud services enable compute.googleapis.com storage.googleapis.com iap.googleapis.com
```

## 2. Backup bucket ☁

Same region as the VM (free tier covers 5 GB in US regions). Soft delete off: a deleted photo must
really leave the bucket (`PLAN.md` §1).

```sh
gcloud storage buckets create gs://$BUCKET --location=$REGION \
  --uniform-bucket-level-access --public-access-prevention --soft-delete-duration=0
```

No lifecycle rule: Litestream removes database history older than 30 days itself, and the photo
copy mirrors deletions.

## 3. Service account for the VM ☁

The VM can read, write and delete objects in this bucket and nothing else.

```sh
gcloud iam service-accounts create saf-vm --display-name="SAF VM"
gcloud storage buckets add-iam-policy-binding gs://$BUCKET \
  --member=serviceAccount:$SA --role=roles/storage.objectUser
```

## 4. Address and VM ☁

```sh
gcloud compute addresses create saf-ip --region=$REGION
gcloud compute instances create saf --zone=$ZONE --machine-type=e2-micro \
  --image-family=debian-13 --image-project=debian-cloud \
  --boot-disk-size=10GB --boot-disk-type=pd-standard \
  --address=saf-ip --service-account=$SA --scopes=cloud-platform \
  --shielded-secure-boot --shielded-vtpm --shielded-integrity-monitoring
```

`pd-standard` matters: the console's default "balanced" disk is not free.

## 5. Firewall ☁

The default network opens SSH and RDP to the whole internet. Close them, then allow exactly three
things: HTTPS from Brazil, port 80 from anywhere (Let's Encrypt checks from abroad; it only answers
those checks and redirects to HTTPS), and SSH from Google's IAP tunnel only.

```sh
gcloud compute firewall-rules delete default-allow-ssh default-allow-rdp --quiet

gcloud compute network-firewall-policies create saf-policy --global
gcloud compute network-firewall-policies rules create 1000 --firewall-policy=saf-policy \
  --global-firewall-policy --direction=INGRESS --action=allow \
  --layer4-configs=tcp:443 --src-region-codes=BR --description="HTTPS, só do Brasil"
gcloud compute network-firewall-policies rules create 1010 --firewall-policy=saf-policy \
  --global-firewall-policy --direction=INGRESS --action=allow \
  --layer4-configs=tcp:80 --src-ip-ranges=0.0.0.0/0 --description="Let's Encrypt e redirecionamento"
gcloud compute network-firewall-policies rules create 1020 --firewall-policy=saf-policy \
  --global-firewall-policy --direction=INGRESS --action=allow \
  --layer4-configs=tcp:22 --src-ip-ranges=35.235.240.0/20 --description="SSH só pelo IAP"
gcloud compute network-firewall-policies associations create --firewall-policy=saf-policy \
  --network=default --global-firewall-policy
```

Anything else coming in is dropped by the network's implied deny. The geolocation rule is billed
per GB it evaluates: cents a month.

## 6. Domain

```sh
gcloud compute addresses describe saf-ip --region=$REGION --format='value(address)'
```

At duckdns.org, create the subdomain and set its IP to that address. The address is static, so no
updater is needed.

## 7. Prepare the VM

```sh
gcloud compute ssh saf --zone=$ZONE --tunnel-through-iap
```

On the VM:

```sh
sudo apt-get update && sudo apt-get install -y docker.io unattended-upgrades
cat /etc/apt/apt.conf.d/20auto-upgrades   # both lines "1"; if not: sudo dpkg-reconfigure -plow unattended-upgrades
sudo mkdir -p /srv/saf/data /etc/saf
sudo chown 65532:65532 /srv/saf/data && sudo chmod 700 /srv/saf/data
gcloud --version                          # preinstalled on Debian images; used for the photo copy
```

**The key.** On the laptop, `go run ./cmd/saf keygen` prints a new one. Save it in the password
manager (and with the second custodian) before anything else. On the VM, paste it in and press
Ctrl+D; the container's user (uid 65532) must be able to read it, nobody else:

```sh
sudo install -o 65532 -g 65532 -m 400 /dev/stdin /etc/saf/key
```

Never copy the key into the bucket, the repo or a chat.

**The settings**, from `deploy/saf.env.example` and `deploy/backup.env.example`:

```sh
printf 'SAF_DOMAIN=%s\nSAF_REPLICA_URL=gs://%s/db\n' saf-farmacia.duckdns.org saf-farmacia-backup | sudo tee /etc/saf/saf.env
echo 'SAF_BUCKET=saf-farmacia-backup' | sudo tee /etc/saf/backup.env
```

## 8. Ship the image and start

On the laptop (CI has already scanned the same Dockerfile):

```sh
make release
gunzip -c dist/saf-release.tar.gz | gcloud compute ssh saf --zone=$ZONE --tunnel-through-iap --command='sudo docker load'
```

On the VM:

```sh
sudo docker run -d --name saf --restart=always --read-only \
  -p 80:8080 -p 443:8443 \
  -v /srv/saf/data:/data -v /etc/saf/key:/etc/saf/key:ro \
  --env-file /etc/saf/saf.env \
  --log-opt max-size=10m --log-opt max-file=5 \
  saf:release
sudo docker logs saf
```

Expect `SAF listening addr=[::]:8443 https=true`, the same for 8080, and Litestream's
`replicating to type=gs`. Open `https://$DOMAIN`: the first visit fetches the certificate and takes a
few seconds.

## 9. First users

Log in as `admin` / `primeiro-acesso` and set up the real administrator on the page that opens;
`admin` stops working after that. Create the view-only user under Usuários. Then load the current
stock as "Saldo inicial" entries.

## 10. Photo copy to the bucket

From the laptop:

```sh
gcloud compute scp deploy/saf-photos-backup.service deploy/saf-photos-backup.timer saf:~ \
  --zone=$ZONE --tunnel-through-iap
```

On the VM:

```sh
sudo mv ~/saf-photos-backup.* /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now saf-photos-backup.timer
sudo systemctl start saf-photos-backup.service && sudo journalctl -u saf-photos-backup --no-pager | tail
```

## 11. Check

- `https://$DOMAIN` shows the login with a valid certificate; `http://$DOMAIN` redirects to it.
- `curl -sI https://$DOMAIN/login` shows `Strict-Transport-Security` and `Content-Security-Policy`.
- A phone on mobile data opens it. Through a VPN abroad, port 443 does not answer.
- `ssh <the IP>` times out; `gcloud compute ssh … --tunnel-through-iap` works.
- `gcloud storage ls gs://$BUCKET/db/` lists Litestream files; `gs://$BUCKET/photos/` fills within
  the hour once there are photos.

## 12. Restore drill: now, then every 3 months

On the laptop, into a folder you delete afterwards: it holds real patient data.

```sh
gcloud auth application-default login   # credentials for Litestream
mkdir -p ~/saf-drill/data && cd ~/saf-drill
make -C ~/projects/saf image            # saf:dev carries the same Litestream as production
docker run --rm --read-only --user "$(id -u)" -v "$PWD/data:/data" \
  -v ~/.config/gcloud:/gcloud:ro -e GOOGLE_APPLICATION_CREDENTIALS=/gcloud/application_default_credentials.json \
  --entrypoint /litestream saf:dev restore -o /data/saf.db gs://$BUCKET/db
gcloud storage rsync --recursive gs://$BUCKET/photos data/photos
# the key from the password manager into ~/saf-drill/key, then:
cd ~/projects/saf && SAF_DATA_DIR=~/saf-drill/data SAF_KEY_FILE=~/saf-drill/key go run ./cmd/saf serve
```

Log in at http://localhost:8080 and compare a few recent movements and a photo with production.
Then stop it and delete `~/saf-drill`.

## 13. Updating SAF

```sh
make release
gunzip -c dist/saf-release.tar.gz | gcloud compute ssh saf --zone=$ZONE --tunnel-through-iap \
  --command='sudo docker tag saf:release saf:previous && sudo docker load'
```

On the VM, remove the container and start it again with the command from step 8. Pending database
migrations run at start, after a copy to `/srv/saf/data/backups/pre-migration-NNN.db`.

When CI's weekly Trivy scan flags Litestream, refresh its libraries first: `make litestream-deps
litestream-test check scan`, commit, then release as above.

**Rollback:** remove the container and start it with `saf:previous` instead of `saf:release`. If a
migration ran, restore its pre-migration copy first (a migration is forward-only).

## 14. If the VM is lost

1. Steps 4 and 7 again (the bucket, firewall, service account and domain survive), then ship the
   image as in step 8, without starting it.
2. Restore the database and photos on the new VM:

   ```sh
   sudo docker run --rm --read-only -v /srv/saf/data:/data --env-file /etc/saf/saf.env \
     --entrypoint /litestream saf:release restore -config /etc/litestream.yml /data/saf.db
   sudo gcloud storage rsync --recursive gs://saf-farmacia-backup/photos /srv/saf/data/photos
   sudo chown -R 65532:65532 /srv/saf/data
   ```

3. Start the container as in step 8. At most a few seconds of database changes and an hour of
   photos are lost.
