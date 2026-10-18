const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');

const root = path.resolve(__dirname, '..');
const files = ['README.md', 'AGENTS.md', 'docs/architecture.md', 'docs/development-plan.md', 'docs/deployment.md'];
const texts = Object.fromEntries(files.map(f => [f, fs.readFileSync(path.join(root, f), 'utf8')]));

assert.deepEqual(fs.readdirSync(path.join(root, 'docs')).sort(), ['architecture.md', 'deployment.md', 'development-plan.md']);

let links = 0;
for (const [file, text] of Object.entries(texts)) {
  assert(text.endsWith('\n'), `${file}: missing final newline`);
  assert(!/[ \t]+$/m.test(text), `${file}: trailing whitespace`);
  assert.equal((text.match(/^```/gm) || []).length % 2, 0, `${file}: unbalanced fences`);

  const ids = [...text.matchAll(/<a id="([^"]+)"><\/a>/g)].map(m => m[1]);
  assert.equal(new Set(ids).size, ids.length, `${file}: duplicate anchor`);

  for (const m of text.matchAll(/\[[^\]]+\]\(([^)]+)\)/g)) {
    const url = m[1];
    if (/^(?:https?:|mailto:)/.test(url)) continue;
    const [filePart, hash] = url.split('#');
    const target = filePart ? path.resolve(root, path.dirname(file), filePart) : path.join(root, file);
    assert(fs.existsSync(target), `${file}: broken target ${url}`);
    if (hash) {
      const targetText = fs.readFileSync(target, 'utf8');
      assert(targetText.includes(`<a id="${hash}"></a>`), `${file}: broken anchor ${url}`);
    }
    links++;
  }
}

const plan = texts['docs/development-plan.md'];
const stages = ['M0', 'M1', 'M2', 'M3', 'M4', 'M5', 'M6', 'M7', 'M8', 'M9', 'M10', 'M11'];
const counts = [8, 12, 8, 8, 6, 8, 8, 8, 8, 8, 8, 8];
const gates = ['M0-08', 'M1-12', 'M2-08', 'M3-08', 'M4-06', 'M5-08', 'M6-08', 'M7-08', 'M8-08', 'M9-08', 'M10-08', 'M11-08'];
const allowedStates = new Set(['未开始', '进行中', '待验收', '已完成', '阻塞', '延期']);

const tasks = new Map();
const stageCompleted = new Map(stages.map(s => [s, 0]));

for (const line of plan.split('\n')) {
  if (!/^\| M([0-9]|10|11)-\d{2} \|/.test(line)) continue;
  const cells = line.split('|').slice(1, -1).map(s => s.trim());
  assert.equal(cells.length, 5, `malformed task row: ${line}`);
  const [id, output, deps, acceptance, state] = cells;
  assert(!tasks.has(id), `duplicate task ${id}`);
  assert(output.length > 5 && acceptance.length > 5, `${id}: missing delivery/acceptance`);
  assert(allowedStates.has(state), `${id}: invalid state ${state}`);

  const stage = id.split('-')[0];
  if (state === '已完成') {
    stageCompleted.set(stage, stageCompleted.get(stage) + 1);
  }

  const parents = deps.match(/M([0-9]|10|11)-\d{2}/g) || [];
  const index = stages.indexOf(stage);
  if (index > 0) parents.push(gates[index - 1]);
  tasks.set(id, {output, deps, acceptance, state, parents: [...new Set(parents)]});
}
assert.equal(tasks.size, 98);

for (const [id, task] of tasks) {
  for (const dep of task.parents) {
    assert(tasks.has(dep), `${id}: unknown dependency ${dep}`);
    const depStageIndex = stages.indexOf(dep.split('-')[0]);
    const idStageIndex = stages.indexOf(id.split('-')[0]);
    assert(depStageIndex <= idStageIndex, `${id}: backward stage dependency ${dep}`);
  }
}

const done = new Set();
function visit(id, stack = new Set()) {
  assert(!stack.has(id), `dependency cycle at ${id}`);
  if (done.has(id)) return;
  const next = new Set(stack);
  next.add(id);
  for (const dep of tasks.get(id).parents) visit(dep, next);
  done.add(id);
}
for (const id of tasks.keys()) visit(id);

function ancestors(id, found = new Set()) {
  for (const dep of tasks.get(id).parents) {
    if (!found.has(dep)) {
      found.add(dep);
      ancestors(dep, found);
    }
  }
  return found;
}

let totalCompleted = 0;
for (const [index, stage] of stages.entries()) {
  const stageTasks = [...tasks.keys()].filter(id => id.startsWith(stage + '-'));
  assert.equal(stageTasks.length, counts[index]);
  for (let n = 1; n <= counts[index]; n++) {
    assert(tasks.has(`${stage}-${String(n).padStart(2, '0')}`));
  }
  const covered = ancestors(gates[index]);
  for (const id of stageTasks) {
    assert(id === gates[index] || covered.has(id), `${stage} gate does not cover ${id}`);
  }
  const completed = stageCompleted.get(stage);
  totalCompleted += completed;
  assert(
    plan.includes(`| ${stage} | ${counts[index]} | ${completed} |`),
    `${stage}: summary count mismatch (expected ${counts[index]} total, ${completed} completed)`
  );
}

assert(plan.includes(`| 合计 | 98 | ${totalCompleted} |`), `total completed mismatch: ${totalCompleted}`);
assert(!/^\| M([0-9]|10|11) \|/m.test(texts['docs/architecture.md']), 'duplicated milestone tracker in architecture');
assert(!/M([0-9]|10|11)-\d{2}/.test(texts['docs/architecture.md']), 'detailed task duplicated in architecture');
assert(texts['README.md'].includes('docs/development-plan.md'));
assert(texts['AGENTS.md'].includes('docs/development-plan.md'));

console.log(JSON.stringify({
  tasks: tasks.size,
  completed: totalCompleted,
  stageCounts: counts,
  stageCompleted: Object.fromEntries(stageCompleted),
  dependencyDAG: 'acyclic',
  gates: 'all tasks covered',
  validLinks: links,
  progressSource: 'docs/development-plan.md'
}));
