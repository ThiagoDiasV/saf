// Browser smoke test: builds SAF, starts it on a fresh data dir, and clicks through it in headless
// Chrome over the DevTools protocol. Needs Node 23+ (built-in fetch and WebSocket) and Google Chrome;
// no npm packages. Run with `make smoke`; set SHOTS=<dir> to also save screenshots. Not in CI.
import { spawn, execFileSync } from 'node:child_process';
import { mkdtempSync, rmSync, writeFileSync, mkdirSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const CHROME = process.env.CHROME || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
const BASE = 'http://127.0.0.1:8080';
const SHOTS = process.env.SHOTS;
const VIEWER_PASSWORD = 'farmacia-2026!';
const ADMIN_PASSWORD = 'paulo-farmacia-2026';
const sleep = ms => new Promise(r => setTimeout(r, ms));

const dir = mkdtempSync(join(tmpdir(), 'saf-smoke-'));
const bin = join(dir, 'saf');
execFileSync('go', ['build', '-o', bin, './cmd/saf'], { stdio: 'inherit' });
const keyFile = join(dir, 'key');
writeFileSync(keyFile, execFileSync(bin, ['keygen']));
const env = { ...process.env, SAF_DATA_DIR: join(dir, 'data'), SAF_KEY_FILE: keyFile };
const app = spawn(bin, ['serve'], { env, stdio: 'ignore' });

const port = 9400 + Math.floor(Math.random() * 400);
const profile = join(dir, 'chrome');
const chrome = spawn(CHROME, ['--headless=new', `--remote-debugging-port=${port}`, `--user-data-dir=${profile}`,
  '--no-first-run', '--no-default-browser-check', '--hide-scrollbars', 'about:blank'], { stdio: 'ignore' });
if (SHOTS) mkdirSync(SHOTS, { recursive: true });

async function waitFor(what, probe) {
  for (let i = 0; i < 75; i++) {
    try {
      const value = await probe();
      if (value) return value;
    } catch {}
    await sleep(200);
  }
  throw new Error(`${what} did not start`);
}

const results = [];
const consoleErrors = [];
const check = (name, ok, detail = '') => results.push({ name, ok: !!ok, detail: ok ? '' : JSON.stringify(detail) });

let ws;
try {
  await waitFor('saf', async () => (await fetch(`${BASE}/login`)).ok);
  const target = await waitFor('chrome', async () =>
    (await (await fetch(`http://127.0.0.1:${port}/json/list`)).json()).find(t => t.type === 'page'));
  ws = new WebSocket(target.webSocketDebuggerUrl);
  await new Promise(r => ws.addEventListener('open', r, { once: true }));

  let nextId = 0;
  const pending = new Map();
  let loadWaiters = [];
  ws.addEventListener('message', ev => {
    const msg = JSON.parse(ev.data);
    if (msg.id && pending.has(msg.id)) {
      const { resolve, reject } = pending.get(msg.id);
      pending.delete(msg.id);
      msg.error ? reject(new Error(JSON.stringify(msg.error))) : resolve(msg.result);
    } else if (msg.method === 'Page.loadEventFired') {
      loadWaiters.forEach(resolve => resolve());
      loadWaiters = [];
    } else if (msg.method === 'Runtime.exceptionThrown') {
      consoleErrors.push(msg.params.exceptionDetails.exception?.description || msg.params.exceptionDetails.text);
    } else if (msg.method === 'Runtime.consoleAPICalled' && ['error', 'warning'].includes(msg.params.type)) {
      consoleErrors.push(msg.params.args.map(a => a.value ?? a.description).join(' '));
    } else if (msg.method === 'Log.entryAdded' && msg.params.entry.level === 'error'
      && !/status of (403|422)\b/.test(msg.params.entry.text)) { // the test triggers these on purpose
      consoleErrors.push(msg.params.entry.text); // CSP violations land here
    }
  });
  const send = (method, params = {}) => new Promise((resolve, reject) => {
    const id = ++nextId;
    pending.set(id, { resolve, reject });
    ws.send(JSON.stringify({ id, method, params }));
  });
  const js = async body => {
    const r = await send('Runtime.evaluate', { expression: `(async () => { ${body} })()`, awaitPromise: true, returnByValue: true });
    if (r.exceptionDetails) throw new Error(r.exceptionDetails.exception?.description || r.exceptionDetails.text);
    return r.result.value;
  };
  const nextLoad = () => new Promise(resolve => loadWaiters.push(resolve));
  // Runs body (which navigates: a submit, a click on a link) and waits for the new page.
  const navigating = async body => { const loaded = nextLoad(); await js(body); await loaded; await sleep(100); };
  const open = path => navigating(`location.href = '${BASE}${path}'; return 1`);
  const text = selector => js(`return document.querySelector(${JSON.stringify(selector)})?.textContent.replace(/\\s+/g, ' ').trim()`);
  const viewport = (width, height, mobile = false) =>
    send('Emulation.setDeviceMetricsOverride', { width, height, deviceScaleFactor: 1, mobile });
  const shot = async name => {
    if (!SHOTS) return;
    await sleep(300); // let transitions (chevrons, toasts) finish
    const r = await send('Page.captureScreenshot', { format: 'png' });
    writeFileSync(join(SHOTS, `${name}.png`), Buffer.from(r.data, 'base64'));
  };
  // Polls body (an expression that returns something) until it is truthy, e.g. after a fetch.
  const waitUntil = async (what, body) => {
    for (let i = 0; i < 30; i++) {
      const value = await js(body);
      if (value) return value;
      await sleep(100);
    }
    throw new Error(`timed out waiting for ${what}`);
  };
  const login = (user, password) => navigating(`const f = document.querySelector('form[action="/login"]');
    f.login.value = ${JSON.stringify(user)}; f.password.value = ${JSON.stringify(password)}; f.requestSubmit(); return 1`);

  await send('Runtime.enable');
  await send('Page.enable');
  await send('Log.enable');
  await viewport(1280, 900);
  await send('Emulation.setEmulatedMedia', { features: [{ name: 'prefers-color-scheme', value: 'light' }] });

  // ---------- Login ----------
  await open('/estoque');
  check('a visitor is sent to the login page', await js(`return location.pathname`) === '/login');
  await shot('01-login');
  await login('admin', 'senha-errada-123');
  check('wrong password shows the error', await text('.field-error') === 'Usuário ou senha incorretos.');
  check('login is kept after an error', await js(`return document.querySelector('[name=login]').value`) === 'admin');
  check('password toggle shows the password', await js(`document.querySelector('[data-toggle-password]').click();
    return document.getElementById('login-senha').type`) === 'text');
  await login('admin', 'primeiro-acesso');
  check('the first login asks for the real administrator',
    await js(`return location.pathname === '/configurar' && !document.querySelector('.tabs')`));
  await shot('01-primeiro-acesso');
  await navigating(`const f = document.querySelector('form[action="/configurar"]');
    f.name.value = 'Paulo Mendes'; f.login.value = 'paulo.mendes';
    f.password.value = ${JSON.stringify(ADMIN_PASSWORD)}; f.confirm.value = ${JSON.stringify(ADMIN_PASSWORD)};
    f.requestSubmit(); return 1`);
  check('admin lands on Estoque', await js(`return location.pathname`) === '/estoque');
  check('header shows the user', await text('.user-name') === 'Paulo Mendes');
  check('admin sees six tabs', await js(`return document.querySelectorAll('.tabs .tab').length`) === 6);

  // ---------- Theme and menus ----------
  check('theme toggle switches to dark', await js(`document.getElementById('theme-toggle').click();
    return document.documentElement.dataset.theme`) === 'dark');
  await open('/estoque');
  check('theme choice survives a reload', await js(`return document.documentElement.dataset.theme`) === 'dark');
  await js(`document.getElementById('theme-toggle').click(); return 1`);
  const menu = await js(`document.querySelector('[aria-controls=user-menu]').click();
    const opened = !document.getElementById('user-menu').hidden;
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    return { opened, closed: document.getElementById('user-menu').hidden }`);
  check('user menu opens and Escape closes it', menu.opened && menu.closed, menu);

  // ---------- Usuários ----------
  await open('/usuarios');
  await navigating(`const f = document.querySelector('form[action="/usuarios"]');
    f.name.value = 'Thiago Dias'; f.login.value = 'thiago.dias'; f.password.value = ${JSON.stringify(VIEWER_PASSWORD)};
    f.querySelector('[value=viewer]').checked = true; f.requestSubmit(); return 1`);
  check('creating a user shows a toast', await text('.toast') === 'Usuário thiago.dias criado.');
  check('the new user is listed', (await text('table')).includes('Thiago Dias'));
  await shot('02-usuarios');
  const deactivate = `document.querySelector('form[data-confirm="Desativar Thiago Dias?"] button').click(); return 1`;
  await js(deactivate);
  const dialog = await js(`const d = document.getElementById('dlg-confirm');
    return { open: d.open, title: d.querySelector('h2').textContent, ok: d.querySelector('[data-confirm-ok]').textContent }`);
  check('Desativar asks first', dialog.open && dialog.title === 'Desativar Thiago Dias?' && dialog.ok === 'Desativar', dialog);
  await shot('03-confirmar-desativar');
  await js(`document.querySelector('[data-confirm-cancel]').click(); return 1`);
  check('Cancelar keeps the user active', (await js(`return !document.getElementById('dlg-confirm').open`))
    && (await text('table')).includes('Ativo'));
  await js(deactivate);
  await navigating(`document.querySelector('[data-confirm-ok]').click(); return 1`);
  check('Confirmar deactivates', await text('.toast') === 'Thiago Dias desativado.');
  await navigating(`document.querySelector('form[action$="/ativo"] button').click(); return 1`);
  check('Reativar needs no confirmation', await text('.toast') === 'Thiago Dias pode entrar de novo.');

  // ---------- Medicamentos ----------
  await open('/medicamentos');
  const fillMedicine = (ingredient, strength, form, unit) => navigating(`const f = document.querySelector('form[action="/medicamentos"]');
    f.ingredient.value = '${ingredient}'; f.strength.value = '${strength}'; f.dosage_form.value = '${form}'; f.unit.value = '${unit}';
    f.requestSubmit(); return 1`);
  await fillMedicine('Dipirona', '500 mg', 'Comprimido', 'comprimido');
  check('creating a medicine shows a toast', await text('.toast') === 'Dipirona 500 mg comprimido cadastrado. Já aparece em Entrada.');
  await fillMedicine('DIPIRONA', '500mg', 'comprimido', 'caixa');
  check('a duplicate is refused next to the field', (await text('.field-error')).startsWith('Já existe: Dipirona 500 mg comprimido.'));
  await fillMedicine('Paracetmol', '750 mg', 'Comprimido', 'comprimido');
  await navigating(`[...document.querySelectorAll('tbody tr')].find(tr => tr.textContent.includes('Paracetmol'))
    .querySelector('a[href$="/editar"]').click(); return 1`);
  await navigating(`const f = document.querySelector('form.card'); f.ingredient.value = 'Paracetamol'; f.requestSubmit(); return 1`);
  check('editing a medicine shows a toast', await text('.toast') === 'Medicamento atualizado. A alteração ficou registrada no histórico.');
  await js(`document.querySelector('form[data-confirm^="Excluir Paracetamol"] button').click(); return 1`);
  const deleteDialog = await js(`const d = document.getElementById('dlg-confirm');
    return { open: d.open, ok: d.querySelector('[data-confirm-ok]').className }`);
  check('Excluir asks with a red button', deleteDialog.open && deleteDialog.ok === 'btn btn-danger-solid', deleteDialog);
  await navigating(`document.querySelector('[data-confirm-ok]').click(); return 1`);
  check('Confirmar deletes', await text('.toast') === 'Paracetamol 750 mg comprimido excluído.');
  await shot('06-medicamentos');
  await navigating(`const box = document.querySelector('[name=arquivados]'); box.click(); return 1`);
  check('Mostrar arquivados reloads the list at once', await js(`return location.search`) === '?q=&arquivados=1');

  // ---------- Entrada ----------
  await open('/entrada');
  const typing = await js(`const line = document.querySelector('#entry-lines .line');
    const med = line.querySelector('[name=medicine]'); med.value = 'Dipirona 500 mg comprimido';
    med.dispatchEvent(new Event('input', { bubbles: true }));
    const exp = line.querySelector('[name=expiry]'); exp.value = '032027'; exp.dispatchEvent(new Event('input', { bubbles: true }));
    return { unit: line.querySelector('.unit').textContent, expiry: exp.value }`);
  check('picking the medicine shows its unit', typing.unit === 'comprimidos', typing);
  check('validade gets its slash while typing', typing.expiry === '03/2027', typing);
  const lines = await js(`document.querySelector('[data-add-line]').click();
    const added = document.querySelectorAll('#entry-lines .line').length;
    document.querySelectorAll('#entry-lines [data-remove-line]')[1].click();
    return { added, after: document.querySelectorAll('#entry-lines .line').length }`);
  check('a line can be added and removed', lines.added === 2 && lines.after === 1, lines);
  const submitEntry = (lot, expiry, qty) => navigating(`const f = document.querySelector('form[action="/entrada"]');
    f.type.value = 'Doação'; const line = f.querySelector('.line');
    line.querySelector('[name=medicine]').value = 'Dipirona 500 mg comprimido'; line.querySelector('[name=lot]').value = '${lot}';
    line.querySelector('[name=manufacturer]').value = 'EMS'; line.querySelector('[name=expiry]').value = '${expiry}';
    line.querySelector('[name=quantity]').value = '${qty}'; f.requestSubmit(); return 1`);
  await submitEntry('A123', '03/2027', '200');
  check('saving an entry shows a toast', await text('.toast') === 'Entrada registrada com 1 medicamento.');
  await submitEntry('a123', '04/2027', '10');
  check('another expiry for the same lot is refused on the line',
    (await text('.line-errors')) === 'O lote A123 (EMS) já existe com validade 03/2027. Corrija aqui ou edite o lote no Estoque.');
  await shot('07-entrada-conflito');
  await submitEntry('X999', '09/2026', '20');

  // ---------- Estoque ----------
  await open('/estoque');
  const collapsed = await js(`return [...document.querySelectorAll('details.med')].map(d => d.open)`);
  check('stock groups start collapsed', collapsed.length === 1 && collapsed.every(open => !open), collapsed);
  const expanded = await js(`const b = document.querySelector('[data-toggle-groups]'); b.click();
    return { open: [...document.querySelectorAll('details.med')].every(d => d.open), label: b.textContent }`);
  check('Expandir todos opens every group', expanded.open && expanded.label === 'Recolher todos', expanded);
  check('Dipirona shows 220 with the expired lot flagged', (await text('.med-total')).startsWith('220 comprimidos')
    && (await text('.med-side')).includes('1 lote vencido'));
  await shot('08-estoque');
  await navigating(`document.querySelector('input[name=validade][value=vencidos]').click(); return 1`);
  check('Vencidos filters at once and opens the group', await js(`return location.search.includes('validade=vencidos')
    && document.querySelector('details.med').open && document.querySelectorAll('tbody tr').length === 1`));
  await open('/estoque');
  await navigating(`document.querySelector('a[aria-label="Editar lote A123"]').click(); return 1`);
  await navigating(`const f = document.querySelector('form.card'); f.lot.value = 'A124'; f.requestSubmit(); return 1`);
  check('editing a lot comes back with its group open', await js(`return location.hash.startsWith('#med-')
    && document.getElementById(location.hash.slice(1)).open`) && await text('.toast') === 'Lote atualizado. A alteração ficou registrada no histórico.');

  // ---------- Saída ----------
  // Dipirona now has X999 (expired, 20) and A124 (03/2027, 200).
  const startExit = async (type, quantity) => {
    await open('/saida');
    await js(`const f = document.querySelector('[data-exit-form]'); f.type.value = '${type}';
      f.type.dispatchEvent(new Event('change', { bubbles: true }));
      const line = f.querySelector('.exit-line');
      const med = line.querySelector('[data-exit-medicine]'); med.value = 'Dipirona 500 mg comprimido';
      med.dispatchEvent(new Event('input', { bubbles: true }));
      const qty = line.querySelector('[data-exit-quantity]'); qty.value = '${quantity}';
      qty.dispatchEvent(new Event('input', { bubbles: true })); return 1`);
    return waitUntil('the lots of the line', `const rows = [...document.querySelectorAll('.exit-line tbody tr')];
      const sum = document.querySelector('.exit-line .sum')?.textContent;
      return rows.length === 2 && sum && rows.map(tr => { const i = tr.querySelector('.take');
        return { lot: tr.cells[0].textContent, take: i.value, disabled: i.disabled }; })`);
  };
  let lots = await startExit('Dispensação', '30');
  check('Saída pre-fills from the closest good lot, the expired one is locked',
    JSON.stringify(lots) === JSON.stringify([{ lot: 'X999', take: '0', disabled: true }, { lot: 'A124', take: '30', disabled: false }]), lots);
  check('the patient field shows for Dispensação', await js(`return !document.getElementById('field-patient').hidden`));
  check('the sum agrees', await text('.exit-line .sum') === '30 de 30 comprimidos');
  const sumAfterEdit = await js(`const take = [...document.querySelectorAll('.exit-line .take')][1]; take.value = '25';
    take.dispatchEvent(new Event('input', { bubbles: true }));
    return { text: document.querySelector('.exit-line .sum').textContent, warn: document.querySelector('.exit-line .sum').classList.contains('sum-warn') }`);
  check('editing a take updates the sum', sumAfterEdit.text === '25 de 30 comprimidos. Faltam 5.' && sumAfterEdit.warn, sumAfterEdit);
  await js(`const take = [...document.querySelectorAll('.exit-line .take')][1]; take.value = '30';
    take.dispatchEvent(new Event('input', { bubbles: true })); return 1`);
  await shot('10-saida');
  await navigating(`const f = document.querySelector('[data-exit-form]'); f.patient_name.value = 'Maria Silva'; f.requestSubmit(); return 1`);
  check('a full name is refused', (await text('#field-patient .field-error')) === 'Use só o primeiro nome, sem sobrenome.');
  await js(`const canvas = document.createElement('canvas'); canvas.width = 2400; canvas.height = 1800;
    const g = canvas.getContext('2d'); g.fillStyle = '#A02D34'; g.fillRect(0, 0, 2400, 1800);
    const blob = await new Promise(resolve => canvas.toBlob(resolve, 'image/jpeg', 0.9));
    const input = document.querySelector('[data-exit-form] [data-photo-input]');
    const transfer = new DataTransfer(); transfer.items.add(new File([blob], 'receita.jpg', { type: 'image/jpeg' }));
    input.files = transfer.files; input.dispatchEvent(new Event('change', { bubbles: true })); return 1`);
  const shrunk = await waitUntil('the photo preview', `const input = document.querySelector('[data-exit-form] [data-photo-input]');
    if (document.querySelectorAll('[data-photo-previews] .thumb').length !== 1 || input.files.length !== 1) return null;
    const bitmap = await createImageBitmap(input.files[0]); return { width: bitmap.width, height: bitmap.height, type: input.files[0].type }`);
  check('a photo is shrunk to 1600 px JPEG before upload', shrunk.width === 1600 && shrunk.height === 1200 && shrunk.type === 'image/jpeg', shrunk);
  await navigating(`const f = document.querySelector('[data-exit-form]'); f.patient_name.value = 'Maria'; f.requestSubmit(); return 1`);
  check('saving a dispensação shows a toast', await text('.toast') === 'Saída registrada: dispensação.');
  lots = await startExit('Vencimento/Descarte', '20');
  check('a discard unlocks the expired lot and takes it first',
    JSON.stringify(lots) === JSON.stringify([{ lot: 'X999', take: '20', disabled: false }, { lot: 'A124', take: '0', disabled: false }]), lots);
  check('the patient field hides for other exits', await js(`return document.getElementById('field-patient').hidden`));
  await navigating(`document.querySelector('[data-exit-form]').requestSubmit(); return 1`);
  check('saving a discard shows a toast', await text('.toast') === 'Saída registrada: vencimento/descarte.');
  await open('/estoque');
  check('Estoque reflects both exits', (await text('.med-total')) === '170 comprimidos');

  // ---------- Histórico ----------
  await open('/historico');
  const historyRows = await js(`return [...document.querySelectorAll('tbody tr')].map(tr => tr.cells[1].textContent.replace(/\\s+/g, ' ').trim())`);
  check('Histórico lists newest first, Ajustes included', historyRows[0] === 'SaídaVencimento/Descarte'
    && historyRows.includes('AjusteLote'), historyRows);
  await navigating(`const f = document.querySelector('form[action="/historico"]'); f.paciente.value = 'maria'; f.requestSubmit(); return 1`);
  check('filtering by first name finds Maria\'s dispensação', await js(`return document.querySelectorAll('tbody tr').length`) === 1
    && (await text('tbody td[data-label="Dispensado para"]')) === 'Maria');
  await shot('11-historico');
  await navigating(`document.querySelector('a[href^="/movimentos/"]').click(); return 1`);
  const metaOf = () => js(`return Object.fromEntries([...document.querySelectorAll('dl.meta dt')].map(dt => [dt.textContent, dt.nextElementSibling.textContent]))`);
  check('detail shows the first name', (await metaOf())['Dispensado para'] === 'Maria');
  const thumb = await waitUntil('the photo thumbnail', `const img = document.querySelector('.thumb-img.blurred img');
    return img && img.complete && img.naturalWidth > 0 && { width: img.naturalWidth, blur: getComputedStyle(img).filter }`);
  check('the photo shows blurred', thumb.width === 1600 && thumb.blur.startsWith('blur'), thumb);
  const photoView = await waitUntil('the photo viewer', `document.querySelector('[data-photo-view]').click();
    const d = document.getElementById('dlg-photo'); const img = d.querySelector('img');
    return d.open && img.complete && img.naturalWidth > 0 && { filter: getComputedStyle(img).filter }`);
  check('clicking opens it in full, not blurred', photoView.filter === 'none', photoView);
  await shot('13-foto');
  await js(`document.querySelector('#dlg-photo [data-close-dialog]').click(); return 1`);
  await navigating(`document.querySelector('a[href$="?editar=observacao"]').click(); return 1`);
  await navigating(`const f = document.querySelector('form[action$="/observacao"]'); f.note.value = 'Receita 123'; f.requestSubmit(); return 1`);
  check('editing the observação is logged', (await text('.toast')).startsWith('Observação atualizada.')
    && (await text('.log')).includes('observação: (vazia) → “Receita 123”'));
  await js(`document.querySelector('form[action$="/estorno"] button').click(); return 1`);
  const reverseDialog = await js(`const d = document.getElementById('dlg-confirm');
    return { open: d.open, body: d.querySelector('[data-confirm-body]').textContent }`);
  check('Estornar asks first and says what goes back', reverseDialog.open
    && reverseDialog.body.startsWith('Devolve ao estoque: 30 comprimidos do lote A124'), reverseDialog);
  await shot('12-detalhe-estornar');
  await navigating(`document.querySelector('[data-confirm-ok]').click(); return 1`);
  check('the estorno is recorded', await text('.toast') === 'Saída estornada. O estoque foi atualizado.'
    && 'Estornada em' in await metaOf());
  await open('/historico?tipo=in&de=&ate=');
  // X999's 20 units were all discarded, so its entry cannot be undone.
  await navigating(`[...document.querySelectorAll('tbody tr')]
    .find(tr => tr.querySelector('[data-label="Itens"]').textContent === 'Dipirona 500 mg comprimido · 20')
    .querySelector('a[href^="/movimentos/"]').click(); return 1`);
  check('an entry whose units left cannot be reversed', (await text('.detail-actions')).startsWith('Não dá para estornar:'));
  await open('/estoque');
  check('Estoque counts the returned units', (await text('.med-total')) === '200 comprimidos');

  // ---------- Viewer ----------
  await js(`document.querySelector('[aria-controls=user-menu]').click(); return 1`);
  await navigating(`document.querySelector('form[action="/sair"] button').click(); return 1`);
  check('Sair goes back to login', await js(`return location.pathname`) === '/login');
  await login('thiago.dias', VIEWER_PASSWORD);
  const viewer = await js(`return { tabs: [...document.querySelectorAll('.tabs .tab')].map(a => a.textContent.trim()),
    badge: document.querySelector('.role-badge')?.textContent.trim() }`);
  check('viewer sees Estoque and Histórico only', JSON.stringify(viewer.tabs) === '["Estoque","Histórico"]', viewer);
  check('viewer sees the view-only badge', viewer.badge === 'Somente visualização', viewer);
  await shot('04-visualizacao');
  await open('/usuarios');
  check('viewer gets Sem permissão on Usuários', await text('h1') === 'Sem permissão');
  await open('/historico?paciente=maria&de=&ate=');
  await navigating(`document.querySelector('a[href^="/movimentos/"]').click(); return 1`);
  check('viewer reads the detail but cannot edit or reverse', (await metaOf())['Dispensado para'] === 'Maria'
    && await js(`return !document.querySelector('a[href$="?editar=observacao"]') && !document.querySelector('form[action$="/estorno"]')`));

  // ---------- Phone ----------
  await js(`document.querySelector('[aria-controls=user-menu]').click(); return 1`);
  await navigating(`document.querySelector('form[action="/sair"] button').click(); return 1`);
  await login('admin', 'primeiro-acesso');
  check('admin no longer logs in after the setup', await text('.field-error') === 'Usuário ou senha incorretos.');
  await login('paulo.mendes', ADMIN_PASSWORD);
  await viewport(390, 844, true);
  await open('/estoque');
  await js(`document.querySelector('[data-toggle-groups]').click(); return 1`);
  await shot('09-celular-estoque');
  const phone = await js(`document.querySelector('[aria-controls=more-menu]').click();
    return { order: getComputedStyle(document.querySelector('.tabs')).order,
      more: [...document.querySelectorAll('#more-menu a')].map(a => a.textContent.trim()), open: !document.getElementById('more-menu').hidden }`);
  check('phone: bottom tabs with Medicamentos and Usuários under Mais',
    phone.order === '1' && phone.open && JSON.stringify(phone.more) === '["Medicamentos","Usuários"]', phone);
  await shot('05-celular-mais');
} catch (e) {
  results.push({ name: 'harness', ok: false, detail: e.stack || String(e) });
} finally {
  ws?.close();
  chrome.kill();
  app.kill();
  await sleep(300);
  rmSync(dir, { recursive: true, force: true });
}

for (const r of results) console.log(`${r.ok ? 'PASS' : 'FAIL'}  ${r.name}${r.detail ? '  ' + r.detail : ''}`);
console.log(`\n${results.filter(r => r.ok).length}/${results.length} passed`);
console.log(consoleErrors.length ? `Console errors:\n${consoleErrors.join('\n')}` : 'No console errors');
process.exit(results.every(r => r.ok) && !consoleErrors.length ? 0 : 1);
