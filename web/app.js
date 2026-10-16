/** Normalize user-entered money without binary floating-point arithmetic. */
export function normalizeAmount(input) {
  const value = String(input).trim().replace(',', '.');
  if (!/^\d{1,9}(?:\.\d{1,2})?$/.test(value)) return null;
  const [integer, decimal = ''] = value.split('.');
  const cents = BigInt(integer) * 100n + BigInt(decimal.padEnd(2, '0'));
  if (cents <= 0n) return null;
  return `${cents / 100n}.${String(cents % 100n).padStart(2, '0')}`;
}

/** Format a server decimal string exactly, including large amounts. */
export function formatMoney(value) {
  const match = /^(\d+)(?:\.(\d{1,2}))?$/.exec(String(value));
  if (!match) return '—';
  const whole = match[1].replace(/^0+(?=\d)/, '').replace(/\B(?=(\d{3})+(?!\d))/g, '\u00a0');
  return `${whole},${(match[2] || '').padEnd(2, '0')}\u00a0₽`;
}

export function isValidDate(value) {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(value) || value.slice(0, 4) === '0000') return false;
  const date = new Date(`${value}T12:00:00Z`);
  return !Number.isNaN(date.getTime()) && date.toISOString().slice(0, 10) === value;
}

export function localToday(date = new Date()) {
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`;
}

export function formatDate(value) {
  if (!isValidDate(value)) return value || '—';
  return new Intl.DateTimeFormat('ru-RU', { day: 'numeric', month: 'short', year: 'numeric', timeZone: 'UTC' }).format(new Date(`${value}T12:00:00Z`));
}

export function buildQuery(filters, page = 1) {
  const params = new URLSearchParams();
  for (const name of ['user_id', 'from', 'to', 'category', 'q']) {
    const value = String(filters[name] || '').trim();
    if (value) params.set(name, value);
  }
  params.set('page', String(page));
  params.set('page_size', '20');
  return params.toString();
}

export function parseUserID(value) {
  if (!/^[1-9]\d{0,15}$/.test(String(value).trim())) return null;
  const id = Number(String(value).trim());
  return Number.isSafeInteger(id) && id <= 4503599627370495 ? id : null;
}

if (typeof document !== 'undefined') initializeDashboard();

function initializeDashboard() {
  const byId = (id) => document.getElementById(id);
  const state = { users: [], categories: [], filters: {}, page: 1, total: 0, loading: false, metadataReady: false, controller: null, editID: null, deleteExpense: null, saving: false, deleting: false, focusReturn: null, toastTimer: null };
  const icons = { 'еда': '♧', 'транспорт': '↗', 'жильё': '⌂', 'развлечения': '✦', 'здоровье': '♡', 'покупки': '▱', 'другое': '◌' };

  async function api(path, options = {}) {
    let response;
    try {
      response = await fetch(path, { ...options, headers: { ...(options.body ? { 'Content-Type': 'application/json' } : {}), ...options.headers } });
    } catch (error) {
      if (error.name === 'AbortError') throw error;
      throw new Error('Не удалось подключиться к серверу. Проверьте, что сервис запущен.');
    }
    if (response.status === 204) return null;
    let result;
    try { result = await response.json(); } catch { throw new Error('Сервер вернул неожиданный ответ. Повторите попытку.'); }
    if (!response.ok) throw new Error(result?.error?.message || 'Не удалось выполнить действие. Повторите попытку.');
    return result;
  }

  function setError(id, message = '') {
    const element = byId(id);
    element.textContent = message;
    element.hidden = !message;
  }

  function userLabel(id) {
    return state.users.find((user) => String(user.id) === String(id))?.label || `Пользователь ${id}`;
  }

  function renderMetadata() {
    const filterUser = byId('filter-user');
    const oldUser = filterUser.value;
    filterUser.replaceChildren(new Option('Вся семья', ''));
    byId('family-users').replaceChildren();
    for (const user of state.users) {
      filterUser.append(new Option(`${user.label} · ${user.id}`, String(user.id)));
      const option = new Option(user.label, String(user.id));
      byId('family-users').append(option);
    }
    if ([...filterUser.options].some((option) => option.value === oldUser)) filterUser.value = oldUser;
    else if (oldUser) {
      filterUser.append(new Option(`Пользователь ${oldUser} · ${oldUser}`, oldUser));
      filterUser.value = oldUser;
    }
    const filterCategory = byId('filter-category');
    const oldCategory = filterCategory.value;
    filterCategory.replaceChildren(new Option('Все категории', ''));
    byId('expense-category').replaceChildren();
    for (const category of state.categories) {
      filterCategory.append(new Option(category, category));
      byId('expense-category').append(new Option(category, category));
    }
    filterCategory.value = oldCategory;
    byId('add-expense').disabled = false;
  }

  async function loadMetadata() {
    const [categories, users] = await Promise.all([api('/api/categories'), api('/api/users')]);
    state.categories = categories.items;
    state.users = users.items;
    state.metadataReady = true;
    renderMetadata();
  }

  function setListState(title, description, loading = false) {
    byId('list-state').hidden = false;
    byId('list-state').classList.toggle('loading', loading);
    byId('state-title').textContent = title;
    byId('state-description').textContent = description;
  }

  function renderRows(items) {
    const rows = byId('expense-rows');
    rows.replaceChildren();
    for (const expense of items) {
      const row = byId('expense-row-template').content.firstElementChild.cloneNode(true);
      row.querySelector('.category-icon').textContent = icons[expense.category] || icons['другое'];
      row.querySelector('.expense-comment').textContent = expense.comment || expense.category;
      row.querySelector('.expense-comment').title = expense.comment || expense.category;
      row.querySelector('.expense-category').textContent = expense.category;
      row.querySelector('.owner-label').textContent = userLabel(expense.user_id);
      row.querySelector('.owner-id').textContent = `ID ${expense.user_id}`;
      row.querySelector('.expense-date').textContent = formatDate(expense.spent_at);
      row.querySelector('.expense-amount').textContent = formatMoney(expense.amount);
      const edit = row.querySelector('.edit-expense');
      edit.setAttribute('aria-label', `Изменить: ${expense.comment || expense.category}, ${formatMoney(expense.amount)}`);
      edit.addEventListener('click', () => openExpense(expense, edit));
      const remove = row.querySelector('.delete-expense');
      remove.setAttribute('aria-label', `Удалить: ${expense.comment || expense.category}, ${formatMoney(expense.amount)}`);
      remove.addEventListener('click', () => openDelete(expense, remove));
      rows.append(row);
    }
    byId('list-state').hidden = items.length > 0;
    if (items.length === 0) {
      const filtered = Object.values(state.filters).some(Boolean);
      setListState(filtered ? 'По этим фильтрам расходов нет' : 'Здесь появятся расходы семьи', filtered ? 'Измените период, категорию или члена семьи. Можно также сбросить все фильтры.' : 'Добавьте первый расход здесь или отправьте боту сообщение, например «кофе 350».');
    }
  }

  function renderPagination() {
    const pageCount = Math.max(1, Math.ceil(state.total / 20));
    byId('pagination').hidden = state.total <= 20;
    byId('page-caption').textContent = `Страница ${state.page} из ${pageCount}`;
    byId('previous-page').disabled = state.loading || state.page <= 1;
    byId('next-page').disabled = state.loading || state.page >= pageCount;
  }

  async function loadExpenses() {
    state.controller?.abort();
    const controller = new AbortController();
    state.controller = controller;
    state.loading = true;
    byId('page-error').hidden = true;
    byId('table-container').setAttribute('aria-busy', 'true');
    byId('expense-rows').replaceChildren();
    byId('total-amount').textContent = '—';
    byId('total-count').textContent = '—';
    byId('results-caption').textContent = 'Загружаем расходы…';
    setListState('Загружаем расходы', 'Собираем данные семьи.', true);
    renderPagination();
    try {
      if (!state.metadataReady) await loadMetadata();
      if (controller.signal.aborted) return;
      const query = buildQuery(state.filters, state.page);
      const [list, summary] = await Promise.all([api(`/api/expenses?${query}`, { signal: controller.signal }), api(`/api/summary?${query}`, { signal: controller.signal })]);
      if (controller.signal.aborted) return;
      state.total = list.total;
      // Deleting the last item on a page moves back to the last existing page.
      const lastPage = Math.max(1, Math.ceil(list.total / 20));
      if (state.page > lastPage) { state.page = lastPage; await loadExpenses(); return; }
      renderRows(list.items);
      byId('total-amount').textContent = formatMoney(summary.amount);
      byId('total-count').textContent = new Intl.NumberFormat('ru-RU').format(summary.count);
      byId('total-caption').textContent = state.filters.from || state.filters.to ? `${state.filters.from ? formatDate(state.filters.from) : 'Начало'} — ${state.filters.to ? formatDate(state.filters.to) : 'без ограничения'}` : 'За всё время · по выбранным фильтрам';
      byId('results-caption').textContent = list.total ? `${new Intl.NumberFormat('ru-RU').format(list.total)} ${pluralExpenses(list.total)}` : 'Пока нет расходов';
    } catch (error) {
      if (error.name === 'AbortError' || controller.signal.aborted) return;
      byId('page-error-text').textContent = error.message;
      byId('page-error').hidden = false;
      byId('results-caption').textContent = 'Не удалось загрузить данные';
      setListState('Данные временно недоступны', 'Повторите загрузку с помощью кнопки выше.');
      byId('pagination').hidden = true;
    } finally {
      if (state.controller === controller) {
        state.loading = false;
        byId('table-container').setAttribute('aria-busy', 'false');
        if (byId('page-error').hidden) renderPagination();
      }
    }
  }

  function pluralExpenses(count) {
    const last = count % 10;
    const teen = count % 100;
    return last === 1 && teen !== 11 ? 'расход' : last >= 2 && last <= 4 && !(teen >= 12 && teen <= 14) ? 'расхода' : 'расходов';
  }

  function toast(message) {
    clearTimeout(state.toastTimer);
    byId('toast').textContent = message;
    byId('toast').hidden = false;
    state.toastTimer = setTimeout(() => { byId('toast').hidden = true; }, 4500);
  }

  function updateUserHint() {
    const id = byId('expense-user').value.trim();
    const existing = state.users.find((user) => String(user.id) === id);
    byId('user-hint').textContent = existing ? `Владелец: ${existing.label} · ID ${existing.id}` : 'Выберите ID из списка или введите Telegram ID вручную.';
  }

  function openExpense(expense = null, trigger = byId('add-expense')) {
    if (state.saving || state.deleting) return;
    state.editID = expense?.id ?? null;
    state.focusReturn = trigger;
    byId('expense-form').reset();
    for (const input of byId('expense-form').querySelectorAll('[aria-invalid]')) input.removeAttribute('aria-invalid');
    byId('dialog-title').textContent = expense ? 'Изменить расход' : 'Добавить расход';
    byId('save-expense').textContent = expense ? 'Сохранить изменения' : 'Сохранить расход';
    byId('expense-user').value = expense ? String(expense.user_id) : state.filters.user_id || (state.users.length === 1 ? String(state.users[0].id) : '');
    byId('expense-amount').value = expense ? expense.amount.replace('.', ',') : '';
    byId('expense-date').value = expense ? expense.spent_at : localToday();
    byId('expense-category').value = expense ? expense.category : state.filters.category || 'другое';
    byId('expense-comment').value = expense?.comment || '';
    setError('expense-error');
    updateUserHint();
    byId('expense-dialog').showModal();
    byId(expense || byId('expense-user').value ? 'expense-amount' : 'expense-user').focus();
  }

  function setSaving(saving) {
    state.saving = saving;
    for (const control of byId('expense-form').querySelectorAll('input, select, textarea, button')) control.disabled = saving;
    byId('save-expense').textContent = saving ? 'Сохраняем…' : state.editID === null ? 'Сохранить расход' : 'Сохранить изменения';
  }

  function invalidField(id, message) {
    byId(id).setAttribute('aria-invalid', 'true');
    setError('expense-error', message);
    byId(id).focus();
  }

  byId('expense-form').addEventListener('submit', async (event) => {
    event.preventDefault();
    if (state.saving) return;
    setError('expense-error');
    for (const input of byId('expense-form').querySelectorAll('[aria-invalid]')) input.removeAttribute('aria-invalid');
    const userID = parseUserID(byId('expense-user').value);
    if (userID === null) return invalidField('expense-user', 'Введите положительный Telegram ID: только цифры, без пробелов.');
    const amount = normalizeAmount(byId('expense-amount').value);
    if (amount === null) return invalidField('expense-amount', 'Введите сумму от 0,01 до 999 999 999,99 ₽, не более двух цифр после запятой. Например, 350,50.');
    const spentAt = byId('expense-date').value;
    if (!isValidDate(spentAt)) return invalidField('expense-date', 'Укажите корректную дату расхода.');
    const category = byId('expense-category').value;
    if (!state.categories.includes(category)) return invalidField('expense-category', 'Выберите категорию из списка.');
    const input = { user_id: userID, amount, category, spent_at: spentAt, comment: byId('expense-comment').value.trim() };
    const wasEditing = state.editID !== null;
    setSaving(true);
    try {
      await api(wasEditing ? `/api/expenses/${encodeURIComponent(state.editID)}` : '/api/expenses', { method: wasEditing ? 'PUT' : 'POST', body: JSON.stringify(input) });
      byId('expense-dialog').close();
      toast(wasEditing ? 'Изменения сохранены' : 'Расход добавлен');
      // Refresh family names/IDs as manual creation can introduce a new owner.
      state.metadataReady = false;
      if (!wasEditing) state.page = 1;
      await loadExpenses();
      if (!state.focusReturn?.isConnected && document.activeElement === document.body) byId('add-expense').focus();
    } catch (error) { setError('expense-error', error.message); } finally { setSaving(false); }
  });

  function openDelete(expense, trigger) {
    if (state.saving || state.deleting) return;
    state.deleteExpense = expense;
    state.focusReturn = trigger;
    byId('delete-description').textContent = `${expense.comment || expense.category} · ${formatMoney(expense.amount)} · ${formatDate(expense.spent_at)}. Владелец: ${userLabel(expense.user_id)} (ID ${expense.user_id}).`;
    setError('delete-error');
    byId('delete-dialog').showModal();
    byId('delete-dialog').querySelector('[data-close]').focus();
  }

  byId('delete-form').addEventListener('submit', async (event) => {
    event.preventDefault();
    if (state.deleting || !state.deleteExpense) return;
    state.deleting = true;
    for (const button of byId('delete-form').querySelectorAll('button')) button.disabled = true;
    byId('confirm-delete').textContent = 'Удаляем…';
    setError('delete-error');
    try {
      await api(`/api/expenses/${encodeURIComponent(state.deleteExpense.id)}`, { method: 'DELETE' });
      byId('delete-dialog').close();
      toast('Расход удалён');
      state.metadataReady = false;
      await loadExpenses();
      if (!state.focusReturn?.isConnected && document.activeElement === document.body) byId('add-expense').focus();
    } catch (error) { setError('delete-error', error.message); } finally {
      state.deleting = false;
      for (const button of byId('delete-form').querySelectorAll('button')) button.disabled = false;
      byId('confirm-delete').textContent = 'Удалить';
    }
  });

  byId('filters').addEventListener('submit', (event) => {
    event.preventDefault();
    setError('filter-error');
    const filters = Object.fromEntries(new FormData(event.currentTarget));
    for (const name of ['from', 'to']) {
      if (filters[name] && !isValidDate(filters[name])) { setError('filter-error', 'Укажите корректные даты периода.'); byId(`filter-${name}`).focus(); return; }
    }
    if (filters.from && filters.to && filters.from > filters.to) { setError('filter-error', 'Начало периода должно быть раньше его окончания или совпадать с ним.'); byId('filter-from').focus(); return; }
    state.filters = filters;
    state.page = 1;
    loadExpenses();
  });
  byId('reset-filters').addEventListener('click', () => {
    byId('filters').reset();
    state.filters = {};
    state.page = 1;
    setError('filter-error');
    loadExpenses();
  });
  byId('previous-page').addEventListener('click', () => { if (state.page > 1 && !state.loading) { state.page--; loadExpenses(); } });
  byId('next-page').addEventListener('click', () => { if (state.page * 20 < state.total && !state.loading) { state.page++; loadExpenses(); } });
  byId('add-expense').addEventListener('click', () => openExpense());
  byId('add-expense').disabled = true;
  byId('expense-user').addEventListener('input', updateUserHint);
  byId('retry').addEventListener('click', () => loadExpenses());
  for (const button of document.querySelectorAll('[data-close]')) button.addEventListener('click', () => byId(button.dataset.close).close());
  for (const id of ['expense-dialog', 'delete-dialog']) {
    const dialog = byId(id);
    dialog.addEventListener('cancel', (event) => { if (state.saving || state.deleting) event.preventDefault(); });
    dialog.addEventListener('close', () => {
      if (state.focusReturn?.isConnected) state.focusReturn.focus();
      else byId('add-expense').focus();
    });
  }
  loadExpenses();
}
