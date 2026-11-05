'use strict';

const THEME_KEY = 'saf-tema';

// Loaded in <head> without defer, so a saved theme is applied before the first paint.
const savedTheme = localStorage.getItem(THEME_KEY);
if (savedTheme === 'light' || savedTheme === 'dark') document.documentElement.dataset.theme = savedTheme;

document.addEventListener('DOMContentLoaded', () => {
  // Theme toggle (absent on the login page).
  const prefersDark = matchMedia('(prefers-color-scheme: dark)');
  const themeButton = document.getElementById('theme-toggle');
  const effectiveTheme = () => document.documentElement.dataset.theme || (prefersDark.matches ? 'dark' : 'light');
  function paintThemeToggle() {
    const dark = effectiveTheme() === 'dark';
    const use = themeButton.querySelector('use');
    themeButton.setAttribute('aria-label', dark ? 'Usar tema claro' : 'Usar tema escuro');
    use.setAttribute('href', use.getAttribute('href').replace(/#.*$/, dark ? '#i-sun' : '#i-moon'));
  }
  if (themeButton) {
    themeButton.addEventListener('click', () => {
      const next = effectiveTheme() === 'dark' ? 'light' : 'dark';
      document.documentElement.dataset.theme = next;
      localStorage.setItem(THEME_KEY, next);
      paintThemeToggle();
    });
    prefersDark.addEventListener('change', paintThemeToggle);
    paintThemeToggle();
  }

  // Menus: one open at a time; a click outside, a click on a menu link, or Escape closes them.
  function closeMenus() {
    document.querySelectorAll('.menu').forEach(menu => { menu.hidden = true; });
    document.querySelectorAll('[data-menu]').forEach(button => button.setAttribute('aria-expanded', 'false'));
  }
  document.addEventListener('click', event => {
    const menuButton = event.target.closest('[data-menu]');
    if (menuButton) {
      const menu = document.getElementById(menuButton.getAttribute('aria-controls'));
      const willOpen = menu.hidden;
      closeMenus();
      menu.hidden = !willOpen;
      menuButton.setAttribute('aria-expanded', String(willOpen));
      if (willOpen) menu.querySelector('a, button')?.focus();
      return;
    }
    if (!event.target.closest('.menu-wrap') || event.target.closest('.menu a')) closeMenus();

    const passwordToggle = event.target.closest('[data-toggle-password]');
    if (passwordToggle) {
      const input = document.getElementById(passwordToggle.dataset.togglePassword);
      const show = input.type === 'password';
      input.type = show ? 'text' : 'password';
      passwordToggle.setAttribute('aria-pressed', String(show));
      passwordToggle.setAttribute('aria-label', show ? 'Esconder senha' : 'Mostrar senha');
    }
  });
  document.addEventListener('keydown', event => {
    if (event.key === 'Escape') closeMenus();
  });

  // Forms with data-confirm ask first. Without JavaScript they simply submit.
  const confirmDialog = document.getElementById('dlg-confirm');
  let pendingForm = null;
  document.addEventListener('submit', event => {
    const form = event.target;
    if (!form.dataset.confirm) return;
    event.preventDefault();
    pendingForm = form;
    confirmDialog.querySelector('[data-confirm-title]').textContent = form.dataset.confirm;
    confirmDialog.querySelector('[data-confirm-body]').textContent = form.dataset.confirmBody || '';
    const ok = confirmDialog.querySelector('[data-confirm-ok]');
    ok.textContent = form.dataset.confirmLabel || 'Confirmar';
    ok.className = 'confirmDanger' in form.dataset ? 'btn btn-danger-solid' : 'btn btn-primary';
    confirmDialog.showModal();
  });
  confirmDialog.querySelector('[data-confirm-ok]').addEventListener('click', () => {
    confirmDialog.close();
    pendingForm?.submit(); // submit() skips the submit event, so this does not ask again
  });
  confirmDialog.querySelector('[data-confirm-cancel]').addEventListener('click', () => confirmDialog.close());

  // Filter forms re-run as soon as a checkbox or select changes; text waits for Enter or Buscar.
  document.addEventListener('change', event => {
    const form = event.target.closest('form[data-autosubmit]');
    if (form && event.target.matches('input[type=checkbox], input[type=radio], select')) form.requestSubmit();
  });

  // Validade fields: type digits, get MM/AAAA.
  document.addEventListener('input', event => {
    const input = event.target;
    if (input.dataset.mask === 'expiry') {
      const digits = input.value.replace(/\D/g, '').slice(0, 6);
      input.value = digits.length > 2 ? `${digits.slice(0, 2)}/${digits.slice(2)}` : digits;
    }
    if (input.name === 'medicine') paintUnit(input);
  });

  // Entry lines: the unit next to the quantity follows the chosen medicine.
  function paintUnit(input) {
    const option = [...document.querySelectorAll('#dl-meds option')].find(o => o.value === input.value);
    input.closest('.line').querySelector('.unit').textContent = option ? option.dataset.unit : 'unidades';
  }
  document.querySelectorAll('.line [name=medicine]').forEach(paintUnit);

  // Add a line from its <template>; removing the last line just clears it.
  document.addEventListener('click', event => {
    const add = event.target.closest('[data-add-line]');
    if (add) {
      const lines = document.getElementById(add.dataset.lines);
      lines.append(document.getElementById(add.dataset.addLine).content.cloneNode(true));
      lines.lastElementChild.querySelector('input').focus();
      return;
    }
    const remove = event.target.closest('[data-remove-line]');
    if (remove) {
      const line = remove.closest('.line');
      if (line.parentElement.querySelectorAll('.line').length > 1) {
        line.remove();
      } else {
        line.querySelectorAll('input').forEach(i => { i.value = ''; i.removeAttribute('aria-invalid'); });
        line.querySelector('.line-errors')?.remove();
        paintUnit(line.querySelector('[name=medicine]'));
      }
    }
  });

  // Estoque groups: open the one named in the address (after editing a lot), toggle all at once.
  const groups = [...document.querySelectorAll('details.med')];
  const toggleGroups = document.querySelector('[data-toggle-groups]');
  const paintToggleGroups = () => {
    if (toggleGroups) toggleGroups.textContent = groups.every(g => g.open) ? 'Recolher todos' : 'Expandir todos';
  };
  if (location.hash.startsWith('#med-')) {
    const target = document.getElementById(location.hash.slice(1));
    if (target) target.open = true;
  }
  groups.forEach(g => g.addEventListener('toggle', paintToggleGroups));
  toggleGroups?.addEventListener('click', () => {
    const open = !groups.every(g => g.open);
    groups.forEach(g => { g.open = open; });
    paintToggleGroups();
  });
  paintToggleGroups();

  // Saída: each line asks the server for its lots, pre-filled by closest expiry (FEFO), when its
  // medicine or quantity changes, or the exit type does. Typed takes only update the sum here;
  // the server checks everything again on save.
  const exitForm = document.querySelector('[data-exit-form]');
  if (exitForm) {
    const exitType = exitForm.querySelector('[data-exit-type]');
    const exitLines = document.getElementById('exit-lines');
    const sprite = document.querySelector('use').getAttribute('href').split('#')[0];
    const requests = new WeakMap(); // line → AbortController of its latest request
    const timers = new WeakMap();
    const medicineOption = line => [...document.querySelectorAll('#dl-meds option')]
      .find(o => o.value === line.querySelector('[data-exit-medicine]').value);

    async function refreshLots(line) {
      requests.get(line)?.abort();
      const controller = new AbortController();
      requests.set(line, controller);
      const params = new URLSearchParams({
        linha: line.dataset.index,
        medicamento: line.querySelector('[data-exit-medicine]').value,
        quantidade: line.querySelector('[data-exit-quantity]').value,
        tipo: exitType.value,
      });
      try {
        const res = await fetch(`/saida/lotes?${params}`, { signal: controller.signal });
        if (!res.ok) return;
        line.querySelector('.fefo').innerHTML = await res.text();
        paintSum(line);
      } catch (e) {
        if (e.name !== 'AbortError') throw e;
      }
    }
    const refreshSoon = line => {
      clearTimeout(timers.get(line));
      timers.set(line, setTimeout(() => refreshLots(line), 250));
    };

    function paintSum(line) {
      const sum = line.querySelector('.sum');
      if (!sum) return;
      const option = medicineOption(line);
      const units = n => `${n} ${Math.abs(n) === 1 ? option?.dataset.unitOne ?? 'unidade' : option?.dataset.unit ?? 'unidades'}`;
      const wanted = Number(line.querySelector('[data-exit-quantity]').value) || 0;
      const rows = [...line.querySelectorAll('tbody tr')];
      const taken = rows.reduce((total, tr) => total + (Number(tr.querySelector('.take').value) || 0), 0);
      const usable = rows.filter(tr => !tr.querySelector('.take').disabled).reduce((total, tr) => total + Number(tr.dataset.stock), 0);
      const ok = wanted > 0 && taken === wanted;
      let text;
      if (!wanted) text = 'Informe a quantidade.';
      else if (wanted > usable) text = `Disponível: ${units(usable)}. Faltam ${wanted - usable}.`;
      else if (ok) text = `${taken} de ${units(wanted)}`;
      else if (taken < wanted) text = `${taken} de ${units(wanted)}. Faltam ${wanted - taken}.`;
      else text = `${taken} de ${units(wanted)}. ${taken - wanted} a mais.`;
      sum.className = 'sum ' + (ok ? 'sum-ok' : wanted ? 'sum-warn' : 'sum-neutral');
      sum.innerHTML = `<svg class="icon" aria-hidden="true"><use href="${sprite}#i-${ok ? 'check' : 'alert'}"/></svg>`;
      sum.append(text);
    }
    const paintUnitOf = line => {
      line.querySelector('.exit-line-head .unit').textContent = medicineOption(line)?.dataset.unit ?? 'unidades';
    };

    exitForm.addEventListener('input', event => {
      const line = event.target.closest('.exit-line');
      if (!line) return;
      if (event.target.matches('[data-exit-medicine]')) {
        paintUnitOf(line);
        refreshSoon(line);
      } else if (event.target.matches('[data-exit-quantity]')) {
        refreshSoon(line);
      } else if (event.target.matches('.take')) {
        paintSum(line);
      }
    });
    exitType.addEventListener('change', () => {
      document.getElementById('field-patient').hidden = exitType.value !== 'Dispensação';
      exitLines.querySelectorAll('.exit-line').forEach(line => {
        if (line.querySelector('[data-exit-medicine]').value) refreshLots(line);
      });
    });
    exitForm.addEventListener('click', event => {
      if (event.target.closest('[data-add-exit-line]')) {
        const index = Number(exitLines.dataset.nextIndex);
        exitLines.dataset.nextIndex = index + 1;
        exitLines.append(document.getElementById('exit-line-template').content.cloneNode(true));
        const line = exitLines.lastElementChild;
        line.dataset.index = index;
        line.querySelector('[data-exit-medicine]').name = `lines.${index}.medicine`;
        line.querySelector('[data-exit-quantity]').name = `lines.${index}.quantity`;
        line.querySelector('[data-exit-medicine]').focus();
        return;
      }
      const remove = event.target.closest('[data-remove-exit-line]');
      if (!remove) return;
      const line = remove.closest('.exit-line');
      if (exitLines.querySelectorAll('.exit-line').length > 1) {
        line.remove();
        return;
      }
      line.querySelectorAll('.exit-line-head input').forEach(i => { i.value = ''; i.removeAttribute('aria-invalid'); });
      line.querySelector('.exit-line-errors')?.remove();
      line.querySelector('.fefo').replaceChildren();
      paintUnitOf(line);
    });
    exitLines.querySelectorAll('.exit-line').forEach(paintSum);
  }

  // Photos: shrunk here to 1600 px JPEG before upload (the server re-encodes them too, dropping any
  // GPS or camera data). Picks add up; each preview can be removed before saving.
  const shrinkPhoto = file => new Promise((resolve, reject) => {
    const url = URL.createObjectURL(file);
    const img = new Image();
    img.onload = () => {
      const scale = Math.min(1, 1600 / Math.max(img.naturalWidth, img.naturalHeight));
      const canvas = document.createElement('canvas');
      canvas.width = Math.round(img.naturalWidth * scale);
      canvas.height = Math.round(img.naturalHeight * scale);
      canvas.getContext('2d').drawImage(img, 0, 0, canvas.width, canvas.height);
      URL.revokeObjectURL(url);
      canvas.toBlob(blob => blob
        ? resolve(new File([blob], file.name.replace(/\.[^.]*$/, '') + '.jpg', { type: 'image/jpeg' }))
        : reject(new Error('canvas')), 'image/jpeg', 0.8);
    };
    img.onerror = () => { URL.revokeObjectURL(url); reject(new Error('unreadable')); };
    img.src = url;
  });
  const chosenPhotos = new WeakMap(); // file input → shrunk files
  function setPhotos(input, files) {
    chosenPhotos.set(input, files);
    const transfer = new DataTransfer();
    files.forEach(f => transfer.items.add(f));
    input.files = transfer.files;
    const previews = input.closest('.photos')?.querySelector('[data-photo-previews]');
    if (!previews) return;
    previews.replaceChildren(...files.map((file, i) => {
      const figure = document.createElement('figure');
      figure.className = 'thumb';
      figure.innerHTML = `<span class="thumb-img"><img alt=""></span><figcaption></figcaption>
        <button type="button" class="btn-icon remove-photo" data-remove-photo="${i}" aria-label="Remover foto"></button>`;
      figure.querySelector('img').src = URL.createObjectURL(file);
      figure.querySelector('figcaption').textContent = file.name;
      figure.querySelector('button').innerHTML = `<svg class="icon" aria-hidden="true"><use href="${document.querySelector('use').getAttribute('href').split('#')[0]}#i-x"/></svg>`;
      return figure;
    }));
  }
  document.addEventListener('change', async event => {
    const input = event.target;
    if (!input.matches('[data-photo-input]')) return;
    const kept = [...(chosenPhotos.get(input) ?? [])];
    const unreadable = [];
    for (const file of input.files) {
      try {
        kept.push(await shrinkPhoto(file));
      } catch {
        unreadable.push(file.name);
      }
    }
    setPhotos(input, kept.slice(0, 10));
    const field = input.closest('.field');
    field?.querySelector('.photo-problem')?.remove();
    if (unreadable.length && field) {
      const problem = document.createElement('span');
      problem.className = 'field-error photo-problem';
      problem.textContent = `Não foi possível ler: ${unreadable.join(', ')}. Use JPEG ou PNG.`;
      field.append(problem);
    }
    if ('autoupload' in input.dataset && kept.length) input.form.submit();
  });
  document.addEventListener('click', event => {
    const remove = event.target.closest('[data-remove-photo]');
    if (!remove) return;
    const input = remove.closest('.photos').querySelector('[data-photo-input]');
    const files = [...(chosenPhotos.get(input) ?? [])];
    files.splice(Number(remove.dataset.removePhoto), 1);
    setPhotos(input, files);
  });

  // Photo viewer: the thumbnail stays blurred; the dialog shows the photo in full.
  const photoDialog = document.getElementById('dlg-photo');
  if (photoDialog) {
    document.addEventListener('click', event => {
      const thumb = event.target.closest('[data-photo-view]');
      if (thumb) {
        photoDialog.querySelector('img').src = thumb.dataset.photoView;
        photoDialog.showModal();
      }
      if (event.target.closest('[data-close-dialog]') || event.target === photoDialog) photoDialog.close();
    });
  }

  // Toasts go away on their own.
  const toasts = document.querySelector('.toasts');
  if (toasts) setTimeout(() => toasts.remove(), 5000);
});
