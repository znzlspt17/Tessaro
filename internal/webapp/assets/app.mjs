import { initialState, reduce, groupProblems, createRenderScheduler, updateCandidates, resultLabel, findRunningRun, isStorageError, recoverSnapshot, rejectionLabel, bodyRejectionLabel } from './state.mjs';
const $ = id => document.getElementById(id);
let state = initialState(), stream = null, activeRun = null, displayedRun = null, replayTimer = null, candidates = [], result = null, lastSeq = 0;
let catalogProblems = [], currentProblemId = null, problemSelection = 0;
let nodes = new Map([['root', {code:''}]]), liveContext = null, queue = Promise.resolve(), viewingPast = false, problemGroups = [], metricValues = {}, selectedParent = '';  
const text = (tag, value, className) => { const node = document.createElement(tag); node.textContent = String(value ?? ''); if (className) node.className = className; return node; };
function notice(message) { $('message').textContent = message; }
async function api(url, options) { const response = await fetch(url, options); if (!response.ok) throw new Error(`요청을 처리하지 못했습니다. (${response.status})`); return response.json(); }
function renderPaths() {
  const container = $('paths'); container.replaceChildren();
  const paths = state.view?.paths || [];
  for (let i = 0; i < 3; i++) {
    const path = paths[i], card = document.createElement('article'); card.className = 'path';
    const head = document.createElement('div'); head.className = 'path-head';
    const info = document.createElement('div'); info.append(text('h3', `경로 ${i + 1}${path ? ` · 순위 ${path.rank || i + 1}` : ''}`));
    info.append(text('p', path ? `ID ${path.id || '—'} · 부모 ${path.parentId || path.parent_id || '—'} · 토큰 ${path.token_count ?? '—'}` : '유지된 경로 없음', 'path-meta')); head.append(info);
    if (path) { head.tabIndex = 0; head.setAttribute('role','button'); head.setAttribute('aria-label', '이 경로의 후보 필터: ' + path.id); const choose = () => { selectedParent = path.id; render(); }; head.addEventListener('click', choose); head.addEventListener('keydown', e => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); choose(); } }); if (selectedParent === path.id) card.classList.add('selected'); } const copy = text('button', '코드 복사', 'copy'); copy.type = 'button'; copy.disabled = !path?.code;
    copy.addEventListener('click', async e => { e.stopPropagation(); return (async () => { try { await navigator.clipboard.writeText(path.code); notice('코드를 복사했습니다.'); } catch { notice('복사하지 못했습니다. 브라우저의 클립보드 권한을 확인해 주세요.'); } })(); }); head.append(copy); card.append(head);
    const pre = document.createElement('pre'); pre.className = `code${path ? '' : ' empty-code'}`;
    if (path) {
      const code = String(path.code || ''); const last = String(path.lexeme || path.token || '');
      if (last && code.endsWith(last) && (path.reason === 'forced' || path.reason === 'active' || path.reason === 'model')) {
        pre.append(document.createTextNode(code.slice(0, -last.length))); pre.append(text('span', last, path.reason === 'forced' ? 'forced' : 'model'));
      } else pre.textContent = code || '아직 생성된 코드가 없습니다.';
    } else pre.textContent = '이 순위에 유지된 경로가 없습니다.';
    card.append(pre); container.append(card);
  }
}
function renderHistory() {
  const container = $('history'); container.replaceChildren();
  if (!state.history.length) { container.append(text('p', '아직 기록된 단계가 없습니다.', 'empty')); return; }
  state.history.forEach((snapshot, index) => { const button = text('button', `${snapshot.step ?? index + 1}단계`, state.view === snapshot ? 'selected' : ''); button.type = 'button'; button.setAttribute('aria-pressed', String(state.view === snapshot)); button.addEventListener('click', () => { stopReplay(); state = reduce(state, {type:'select', index}); render(); }); container.append(button); });
}
function renderCandidates() {
  const rejected = $('declaration-rejection'); rejected.hidden = !state.view?.rejection; rejected.textContent = state.view?.rejection ? `최근 선언 차단: ${rejectionLabel(state.view.rejection)} 다른 경로의 탐색은 계속될 수 있습니다.` : ''; const shownCandidates = state.view?.candidates || []; const filter = $('parent-filter'), old = filter.value; filter.replaceChildren(new Option('전체', ''));
  const bodyRejected = $('body-rejection'); bodyRejected.hidden = !state.view?.body_rejection; bodyRejected.textContent = state.view?.body_rejection ? `최근 본문 차단: ${bodyRejectionLabel(state.view.body_rejection)} 다른 경로의 탐색은 계속될 수 있습니다.` : '';
  [...new Set(shownCandidates.map(c => c.parent_id).filter(Boolean))].forEach(id => filter.add(new Option(id, id))); filter.value = selectedParent || old; if (!filter.value) selectedParent = '';
  const context = state.view?.decision_context;
  $('decision-intent').textContent = context?.intent || '기록 없음';
  $('decision-tests').textContent = Array.isArray(context?.public_tests) ? JSON.stringify(context.public_tests, null, 2) : '기록 없음';
  const parents = $('decision-parents'); parents.replaceChildren();
  const parentEntries = Object.entries(context?.parents || {}).filter(([id]) => !filter.value || id === filter.value);
  if (!parentEntries.length) parents.append(text('p', '부모 코드 기록 없음', 'hint'));
  for (const [id, parent] of parentEntries) { const label = text('p', `부모 코드 · ${id}`, 'details-label'); const code = text('pre', typeof parent?.prefix === 'string' ? parent.prefix : '기록 없음', 'decision-parent-code'); parents.append(label, code); }
  const body = $('candidates'); body.replaceChildren(); const visible = shownCandidates.filter(c => !filter.value || c.parent_id === filter.value);
  if (!visible.length) { const row = document.createElement('tr'), cell = text('td', '표시할 후보가 없습니다.', 'empty'); cell.colSpan = 5; row.append(cell); body.append(row); return; }
  // 2026-11-07: 코드와 토큰은 텍스트 노드로 표시해 모델 후보를 HTML로 해석하지 않는다.
  visible.forEach(c => { const row = document.createElement('tr'); row.append(text('td', c.parent_id || '—'), text('td', c.lexeme || '—')); const cell = document.createElement('td'); const pre = document.createElement('pre'); pre.className = 'candidate-code'; const code = typeof c.code === 'string' ? c.code : null, lexeme = c.lexeme || ''; if (code !== null && lexeme && code.endsWith(lexeme)) { pre.append(document.createTextNode(code.slice(0, -lexeme.length)), text('mark', lexeme, 'candidate-token')); } else pre.textContent = code ?? '기록 없음'; cell.append(pre); row.append(cell, text('td', c.score ?? '—'), text('td', ({available:'후보',ranked:'평가 완료',active:'유지',pruned:'탈락',pruned_strict:'탈락',duplicate:'중복',public_passed:'검증 통과',public_failed:'검증 실패'})[c.reason] || '기록 없음')); body.append(row); });
}
function render() {
  renderPaths(); renderHistory(); renderCandidates(); if (result && !state.paused) $('phase').textContent = `문제 결과 · ${resultLabel(result.status)}`; $('step-label').textContent = state.view ? `단계 ${state.view.step ?? '—'}` : '단계 —';
  $('pause').disabled = state.paused || !state.live; $('resume').disabled = !viewingPast && (!state.paused || !state.live);
  $('download-code').disabled = !result?.source; $('download-trace').disabled = !displayedRun; $('download-result').disabled = !displayedRun || (displayedRun === activeRun && !$('stop').disabled); $('final-code').textContent = result?.source || '완료된 코드가 없습니다.'; if (result?.source) $('final-result').open = true; $('metrics').textContent = `모델 요청 ${metricValues.calls ?? '—'} / ${metricValues.max_calls ?? '—'} · 후보 ${metricValues.candidates ?? '—'} / ${metricValues.max_candidates ?? '—'} · 토큰 한도 ${metricValues.max_tokens ?? '—'}`;
}
const scheduleRender = createRenderScheduler(render, requestAnimationFrame);
function setRunLocked(locked) { $('problem').disabled = locked; $('model').disabled = locked; document.querySelectorAll('input[name=scope]').forEach(input => { input.disabled = locked; }); $('start').disabled = locked; $('stop').disabled = !locked; }
function showProblemDetails() { const problem = catalogProblems.find(p => p.id === $('problem').value); $('problem-intent').textContent = problem?.intent || '문제 설명이 없습니다.'; $('problem-tests').textContent = problem?.public_tests?.length ? problem.public_tests.map((test, i) => `${i + 1}. 입력 ${JSON.stringify(test.inputs)} → 기대 ${JSON.stringify(test.want)}`).join('\n') : '공개 사례가 없습니다.'; }
$('problem').addEventListener('change', showProblemDetails);
function stopReplay() { if (replayTimer) clearInterval(replayTimer); replayTimer = null; $('play').textContent = '재생'; }
function selectRelative(offset) { const i = state.history.indexOf(state.view); const index = Math.max(0, Math.min(state.history.length - 1, i + offset)); state = reduce(state, {type:'select', index}); render(); }
$('pause').addEventListener('click', () => { state = reduce(state, {type:'pause'}); notice('화면만 일시정지했습니다. 합성과 모델 호출은 계속 진행됩니다. 실제 중지는 중지 버튼을 누르세요.'); render(); });
$('resume').addEventListener('click', () => { notice(''); stopReplay(); if (viewingPast && activeRun) { displayedRun = activeRun; viewingPast = false; $('follow').checked = true; restoreLive(); } state = reduce(state, {type:'resume'}); render(); });
$('prev').addEventListener('click', () => { stopReplay(); selectRelative(-1); }); $('next').addEventListener('click', () => { stopReplay(); selectRelative(1); });
$('play').addEventListener('click', () => { if (replayTimer) { stopReplay(); return; } if (!state.history.length) return; if (state.history.indexOf(state.view) >= state.history.length - 1) state = reduce(state, {type:'select', index:0}); replayTimer = setInterval(() => { if (state.history.indexOf(state.view) >= state.history.length - 1) { stopReplay(); return; } selectRelative(1); }, 700); $('play').textContent = '재생 멈춤'; render(); });
$('parent-filter').addEventListener('change', () => { selectedParent = $('parent-filter').value; render(); });
$('download-code').addEventListener('click', () => { if (!result?.source) return; const url = URL.createObjectURL(new Blob([result.source], {type:'text/plain;charset=utf-8'})); const a = document.createElement('a'); a.href=url; a.download='result.go'; a.click(); URL.revokeObjectURL(url); });
$('download-result').addEventListener('click', () => { if (displayedRun) location.href = `/api/history/${encodeURIComponent(displayedRun)}/download`; });
$('download-trace').addEventListener('click', () => { if (displayedRun) location.href = `/api/${displayedRun === activeRun ? 'runs' : 'history'}/${encodeURIComponent(displayedRun)}/trace`; });
async function catalog() { const data = await api('/api/catalog'); catalogProblems = data.problems; $('model').replaceChildren(...data.models.map(m => new Option(m.label, m.id))); $('model').value = 'fake'; $('problem').replaceChildren(...data.problems.map(p => new Option(p.label, p.id))); showProblemDetails(); }

async function refreshRuns() { const data = await api('/api/history'); const container = $('runs'); container.replaceChildren(); if (!(data.runs || []).length) container.append(text('p','아직 실험이 없습니다.','empty')); const runningId = findRunningRun(data.runs || [], activeRun); if (runningId) { const snapshot = await api(`/api/runs/${encodeURIComponent(runningId)}`); if (!activeRun && (snapshot.status === 'running' || snapshot.status === 'cancelling')) attachRun(snapshot.run_id); } (data.runs || []).forEach(item => { const b = text('button', `${item.problem || item.run_id} · ${item.status || '저장됨'}`, 'run-item'); b.type='button'; b.addEventListener('click', () => openHistory(item.run_id)); container.append(b); }); }
$('refresh-runs').addEventListener('click', () => refreshRuns().catch(e => notice(e.message)));
function resetView() { currentProblemId = null; state = initialState(); candidates = []; nodes = new Map([['root', {code:''}]]); result = null; }
function saveLive() { return {state, candidates, nodes, result, metricValues, currentProblemId}; }
function restoreLive() { if (liveContext) ({state, candidates, nodes, result, metricValues, currentProblemId} = liveContext); }
// 2026-11-05: 선택한 문제의 구간만 읽어 전체 suite trace를 메모리에 적재하지 않는다.
async function trace(id, running, problemId) {
  const response = await fetch(`/api/${running ? 'runs' : 'history'}/${encodeURIComponent(id)}/trace?problem=${encodeURIComponent(problemId)}`);
  if (!response.ok) throw new Error('단계 기록을 불러오지 못했습니다.');
  return (await response.text()).split('\n').filter(Boolean).map(line => JSON.parse(line));
}
function buildProblemGroups(snapshot) {
  const groups = (snapshot.results || []).map(item => ({problem: catalogProblems.find(p => p.id === item.problem_id) || {id:item.problem_id, label:item.problem_id}, result:item.result}));
  if (snapshot.current && !groups.some(group => group.problem.id === snapshot.current.id)) groups.push({problem:snapshot.current, result:null});
  return groups;
}
async function openHistory(id) {
  try {
    const data = await api(`/api/${id === activeRun ? 'runs' : 'history'}/${encodeURIComponent(id)}`);
    if (displayedRun === activeRun && !viewingPast) liveContext = saveLive();
    displayedRun = id; viewingPast = true; $('follow').checked = false; stopReplay(); resetView();
    problemGroups = buildProblemGroups(data); const currentIndex = problemGroups.findIndex(group => group.problem.id === data.current?.id); const selected = currentIndex >= 0 ? currentIndex : problemGroups.length - 1;
    renderProblemRuns(selected); if (problemGroups.length) await showProblem(selected); else { $('run-summary').textContent = '기록된 문제가 없습니다.'; render(); }
  } catch (e) { notice(e.message); }
}
function renderProblemRuns(selected = -1) {
  const container = $('problem-runs'); container.replaceChildren();
  if (!problemGroups.length) { container.append(text('p', '기록된 문제가 없습니다.', 'empty')); return; }
  problemGroups.forEach((group, index) => { const b = text('button', `${group.problem.label || group.problem.id} · ${group.result ? resultLabel(group.result.status) : '진행 중'}`, 'run-item'); b.type = 'button'; b.setAttribute('aria-pressed', String(index === selected)); b.addEventListener('click', () => showProblem(index)); container.append(b); });
}
async function showProblem(index) {
  const group = problemGroups[index]; if (!group || !displayedRun) return;
  if (displayedRun === activeRun && !viewingPast) liveContext = saveLive();
  viewingPast = true; $('follow').checked = false; stopReplay(); const id = displayedRun, ticket = ++problemSelection;
  renderProblemRuns(index); notice('선택한 문제의 기록을 불러오는 중입니다.');
  try {
    const events = await trace(id, id === activeRun, group.problem.id);
    if (ticket !== problemSelection || id !== displayedRun || !viewingPast) return;
    resetView(); for (const event of events) processUpdate(event, true); result = group.result || result;
    $('current-problem').textContent = group.problem.label || group.problem.id;
    $('run-summary').textContent = `문제 결과: ${result ? resultLabel(result.status) : '진행 중'} · 기록 재생은 모델을 호출하지 않습니다.`; $('phase').textContent = `문제 결과 · ${result ? resultLabel(result.status) : '진행 중'}`;
    notice(events.length ? '' : '이 문제에 기록된 단계가 없습니다.'); render();
  } catch (e) { if (ticket === problemSelection) notice(e.message); }
}
$('refresh-problems').addEventListener('click', async () => {
  if (!displayedRun) return; const id = displayedRun;
  try { const snapshot = await api(`/api/${id === activeRun ? 'runs' : 'history'}/${encodeURIComponent(id)}`); if (id !== displayedRun) return; problemGroups = buildProblemGroups(snapshot); renderProblemRuns(); } catch (e) { notice(e.message); }
});
function processUpdate(event, replay = false) {
  if (!replay && (event.run_id !== activeRun || event.seq <= lastSeq)) return;
  if (!replay) lastSeq = event.seq;
  const hidden = !replay && (displayedRun !== activeRun || viewingPast);
  let shown; if (hidden) { shown = saveLive(); restoreLive(); }
  const d = event.data || {};
  if (event.kind === 'problem_started') { const previousState = state; resetView(); currentProblemId = d.id || null; metricValues = {calls:0, candidates:0, ...(d.budgets || {})}; if (previousState.paused && !replay) state = {...state, paused:true, view:previousState.view, history:previousState.history}; if (!hidden && !replay) { $('current-problem').textContent = d.label || d.id || '문제 실행 중'; $('run-summary').textContent = '탐색 중'; } }
  if (event.kind === 'verification' && !hidden && !replay && !state.paused) $('phase').textContent = `${({typecheck:'타입 검사',compile:'컴파일',public:'공개 테스트',heldout:'비공개 테스트',model:'모델 평가'})[d.phase] || '검증'} · ${({started:'진행 중',passed:'통과',failed:'실패'})[d.status] || d.status || '대기'}`;
  if (event.kind === 'search') { if (!hidden && !replay && !state.paused && d.reason === 'ranked') $('phase').textContent = '모델 평가 · 완료'; if (d.calls != null) metricValues.calls = d.calls; if (d.candidates != null) metricValues.candidates = d.candidates;
    if (!['declaration_rejected','body_rejected'].includes(d.reason) && d.id && d.parent_id && d.lexeme && nodes.has(d.parent_id)) { const parent = nodes.get(d.parent_id).code; nodes.set(d.id, {code: parent ? `${parent} ${d.lexeme}` : d.lexeme, lexeme:d.lexeme, reason:d.reason, parent_id:d.parent_id}); }
    // 2026-11-07: available로 제공된 후보만 모델 요청 표에 올리고 각 단계의 실제 문맥과 함께 보관한다.
    if (d.reason === 'available' || (['ranked','active','pruned','pruned_strict','duplicate','public_passed','public_failed'].includes(d.reason) && candidates.some(c => c.id === d.id))) {
      const node = nodes.get(d.id); candidates = updateCandidates(candidates, {...d, ...(node && typeof node.code === 'string' ? {code:node.code} : {})});
      state = reduce(state, {type:'decision', step:d.step, candidates:[...candidates], decision_context:state.live?.step === d.step ? state.live?.decision_context : undefined});
    }
    if (d.reason === 'decision_context') state = reduce(state, {type:'decision', step:d.step, candidates:[...candidates], decision_context:d.decision_context});
    if (d.reason === 'declaration_rejected') state = reduce(state, {type:'declaration_rejected', step:d.step, rejection_reason:d.rejection_reason});
    if (d.reason === 'body_rejected') state = reduce(state, {type:'body_rejected', step:d.step, rejection_reason:d.rejection_reason});
    if (d.reason === 'frontier') { const paths = (d.frontier || []).map(p => ({...p, ...(nodes.get(p.id) || {})})); const sameStep = state.live?.step === d.step; state = reduce(state, {type:'frontier', step:d.step, paths, candidates: sameStep ? state.live?.candidates || [] : [], decision_context:sameStep ? state.live?.decision_context : undefined, rejection:state.live?.rejection, body_rejection:state.live?.body_rejection}); }
  }
  if (event.kind === 'problem_finished') { result = d.result || null; if (!hidden && !replay && !state.paused) { $('phase').textContent = `문제 결과 · ${resultLabel(result?.status)}`; $('run-summary').textContent = `문제 결과: ${resultLabel(result?.status)}`; } }
  if (!replay && (event.kind === 'run_finished' || event.kind === 'storage_error')) { const failed = isStorageError(event.kind, d); $('connection').textContent = failed ? '저장 실패' : '묶음 실행 종료'; if (failed) notice('실험 결과를 저장하지 못했습니다.'); setRunLocked(false); if (stream) stream.close(); refreshRuns().catch(() => {}); }
  if (hidden) { liveContext = saveLive(); ({state, candidates, nodes, result, metricValues, currentProblemId} = shown); } else { if (!replay) liveContext = saveLive(); if (!state.paused && !replay) scheduleRender(); }
}
// 2026-11-05: 시작과 새로고침 복구가 동일한 기존 실행에 구독하며 새 모델 요청을 만들지 않는다.
function attachRun(id) {
  activeRun = id; displayedRun = id; viewingPast = false; $('follow').checked = true; lastSeq = 0;
  resetView(); liveContext = saveLive(); setRunLocked(true); $('connection').textContent = '실행 중'; notice('');
  if (stream) stream.close(); stream = new EventSource(`/api/runs/${encodeURIComponent(id)}/events`);
  stream.addEventListener('snapshot', e => { const snapshot = JSON.parse(e.data); queue = queue.then(() => applySnapshot(snapshot)).catch(err => notice(err.message)); });
  stream.addEventListener('update', e => { const update = JSON.parse(e.data); queue = queue.then(() => processUpdate(update)).catch(err => notice(err.message)); });
  stream.onerror = () => { $('connection').textContent = '연결 재시도 중'; }; render();
}
$('run-form').addEventListener('submit', async event => {
  event.preventDefault(); setRunLocked(true);
  try {
    const problem = document.querySelector('input[name="scope"]:checked').value === 'all' ? 'all' : $('problem').value;
    const data = await api('/api/runs', {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify({model:$('model').value, problem})});
    attachRun(data.run_id);
  } catch (e) { setRunLocked(false); notice(e.message); }
});
async function applySnapshot(snapshot) {
  if (snapshot.run_id !== activeRun || snapshot.seq < lastSeq) return;
  const hidden = displayedRun !== activeRun || viewingPast; let shown; if (hidden) { shown = saveLive(); restoreLive(); }
  if ($('model').querySelector(`option[value="${CSS.escape(snapshot.model)}"]`)) $('model').value = snapshot.model; if (snapshot.problem !== 'all' && $('problem').querySelector(`option[value="${CSS.escape(snapshot.problem)}"]`)) { $('problem').value = snapshot.problem; showProblemDetails(); } document.querySelector(`input[name=scope][value="${snapshot.problem === 'all' ? 'all' : 'single'}"]`).checked = true;
  const hadSameProblem = currentProblemId !== null && currentProblemId === snapshot.current?.id;
  ({state, nodes, currentProblemId} = recoverSnapshot(state, currentProblemId, snapshot, nodes));
  candidates = snapshot.candidates || [];
  lastSeq = snapshot.seq;
  if (!hadSameProblem && !hidden && snapshot.step > 0) notice('현재 상태를 복구했습니다. 이전 단계는 문제 목록에서 기록을 열어 확인하세요.');
  result = snapshot.results?.findLast(item => item.problem_id === snapshot.current?.id)?.result || result; metricValues = snapshot.metrics || {}; if (!hidden) $('phase').textContent = ({typecheck:'타입 검사',compile:'컴파일',public:'공개 테스트',heldout:'비공개 테스트',model:'모델 평가'})[snapshot.phase] || '탐색 중'; if (!hidden && result) $('phase').textContent = `문제 결과 · ${resultLabel(result.status)}`;
  if (snapshot.status !== 'running' && snapshot.status !== 'cancelling') { const failed = isStorageError('', snapshot); $('connection').textContent = failed ? '저장 실패' : '묶음 실행 종료'; if (failed) notice('실험 결과를 저장하지 못했습니다.'); setRunLocked(false); stream?.close(); refreshRuns().catch(() => {}); }
  if (hidden) { liveContext = saveLive(); ({state, candidates, nodes, result, metricValues, currentProblemId} = shown); } else { liveContext = saveLive(); $('current-problem').textContent = snapshot.current?.label || snapshot.problem; $('run-summary').textContent = `${snapshot.completed || 0} / ${snapshot.total || 1} 문제 종료${result ? ` · 문제 결과: ${resultLabel(result.status)}` : ''}`; render(); }
}
$('stop').addEventListener('click', async () => { if (!activeRun) return; try { await api(`/api/runs/${encodeURIComponent(activeRun)}/cancel`, {method:'POST'}); notice('중지를 요청했습니다.'); } catch (e) { notice(e.message); } });
$('follow').addEventListener('change', () => { if ($('follow').checked && activeRun) { displayedRun = activeRun; viewingPast = false; restoreLive(); $('run-summary').textContent = '현재 실행을 따라가는 중입니다.'; render(); } });
$('start').disabled = true; render(); catalog().then(refreshRuns).then(() => { if (!activeRun) $('start').disabled = false; }).catch(e => { $('start').disabled = false; notice(e.message); });





