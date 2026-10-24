import test from 'node:test';
import assert from 'node:assert/strict';
import { CATEGORY_LABELS, categoryLabel, normalizeAmount, formatMoney, isValidDate, localToday, formatDate, buildQuery, parseUserID } from './app.js';

test('all seven legacy category keys have English display labels without changing API values', () => {
  const expected = [
    ['еда', 'Food'],
    ['транспорт', 'Transport'],
    ['жильё', 'Housing'],
    ['развлечения', 'Entertainment'],
    ['здоровье', 'Health'],
    ['покупки', 'Shopping'],
    ['другое', 'Other'],
  ];
  assert.deepEqual(Object.entries(CATEGORY_LABELS), expected);
  for (const [key, label] of expected) {
    assert.equal(categoryLabel(key), label);
    assert.equal(new URLSearchParams(buildQuery({ category: key })).get('category'), key);
  }
  assert.equal(categoryLabel('unknown'), 'Unknown category');
  assert.equal(categoryLabel('toString'), 'Unknown category');
});

test('money normalization preserves exact cents and accepts dot or comma decimals', () => {
  assert.equal(normalizeAmount(' 000350,5 '), '350.50');
  assert.equal(normalizeAmount('0.01'), '0.01');
  assert.equal(normalizeAmount('999999999.99'), '999999999.99');
  assert.equal(normalizeAmount('500'), '500.00');
  for (const value of ['', '0', '0.00', '-1', '1.234', '1e3', 'Infinity', '12 RUB', '1,000.50', '1000000000.00']) assert.equal(normalizeAmount(value), null, value);
});

test('English RUB formatting never rounds a large server decimal', () => {
  assert.equal(formatMoney('123456789123456789.01'), '123,456,789,123,456,789.01\u00a0₽');
  assert.equal(formatMoney('0.00'), '0.00\u00a0₽');
  assert.equal(formatMoney('0350.5'), '350.50\u00a0₽');
  assert.equal(formatMoney('3.141'), '—');
});

test('dates validate real calendar dates and format without timezone shifts', () => {
  assert.equal(isValidDate('2024-02-29'), true);
  assert.equal(isValidDate('0001-01-01'), true);
  for (const value of ['0000-01-01', '2025-02-29', '2026-04-31', '2026-13-01', '2026-1-2', 'tomorrow', '']) assert.equal(isValidDate(value), false, value);
  assert.equal(formatDate('2026-10-16'), '16 Oct 2026');
  assert.equal(localToday(new Date(2026, 9, 16, 23, 59)), '2026-10-16');
});

test('filter parameters are encoded, bounded by fixed page size, and omit blanks', () => {
  const params = new URLSearchParams(buildQuery({ user_id: '123', from: '2026-10-01', to: '', category: 'еда', q: ' coffee & tea ' }, 2));
  assert.equal(params.get('q'), 'coffee & tea');
  assert.equal(params.get('category'), 'еда'); // Legacy stored category key.
  assert.equal(params.get('page'), '2');
  assert.equal(params.get('page_size'), '20');
  assert.equal(params.has('to'), false);
  assert.equal(params.get('user_id'), '123');
});

test('multilingual user text is preserved in filters and is never translated into a category', () => {
  const comment = 'кофе & чай · café';
  const params = new URLSearchParams(buildQuery({ q: comment }));
  assert.equal(params.get('q'), comment);
  assert.equal(categoryLabel(comment), 'Unknown category');
});

test('Telegram IDs reject imprecise or malformed values', () => {
  assert.equal(parseUserID('123456789'), 123456789);
  assert.equal(parseUserID(' 123 '), 123);
  assert.equal(parseUserID('4503599627370495'), 4503599627370495);
  for (const value of ['', '0', '-123', '01', '12.3', '1e3', '12 34', '4503599627370496', '9007199254740992']) assert.equal(parseUserID(value), null, value);
});
