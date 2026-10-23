// Pure helpers of the setup page's model finder (no DOM): `make test-web`.

// sortModels puts the models that the endpoint says support tool calling
// first, then the rest, each group by id. It returns a new array.
export function sortModels(models) {
  const rank = (m) => (m.tools === true ? 0 : 1);
  return [...(models || [])].sort((a, b) => rank(a) - rank(b) || (a.id < b.id ? -1 : a.id > b.id ? 1 : 0));
}

// datalistOptions gives the <option> value and label of every model: the
// typed value is always the bare id; a tool-capable one is marked.
export function datalistOptions(models) {
  return sortModels(models).map((m) => ({ value: m.id, label: m.tools === true ? m.id + ' — tools' : '' }));
}

// modelsStatus is the line under the Model input: { kind, text } with kind
// ok or fail.
export function modelsStatus(r) {
  if (!r || r.error) {
    const text = [r && r.error, r && r.fix].filter(Boolean).join(' ') || 'Could not list the models. You can still type the model name.';
    return { kind: 'fail', text };
  }
  const n = (r.models || []).length;
  const word = n === 1 ? '1 model found' : n + ' models found';
  if (r.tool_info) {
    const t = r.models.filter((m) => m.tools === true).length;
    return { kind: 'ok', text: word + ', ' + t + ' marked as supporting tool calling' };
  }
  return { kind: 'ok', text: word + '. Check that the one you pick supports tool calling.' };
}

// probeStatus is the inline result of the Test tool calling button.
export function probeStatus(r) {
  if (!r) return { kind: 'fail', text: 'sheepdog did not answer.' };
  if (r.ok) return { kind: 'ok', text: '✓ ' + r.detail };
  return { kind: 'fail', text: '✗ ' + (r.detail || 'The test failed.') + (r.fix ? ' — ' + r.fix : '') };
}
