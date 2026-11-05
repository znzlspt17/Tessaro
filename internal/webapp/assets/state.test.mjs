import { test } from 'node:test';
import { strict as assert } from 'node:assert';
import { initialState, reduce } from './state.mjs';

test('frontier만 코드 창을 갱신하고 일시정지 중에도 최신 상태를 보관한다', () => {
  let state = reduce(initialState(), { type: 'frontier', step: 1, paths: [{ id: 'a', code: '첫째' }] });
  state = reduce(state, { type: 'pause' });
  state = reduce(state, { type: 'candidate', paths: [{ id: 'x', code: '후보' }] });
  state = reduce(state, { type: 'frontier', step: 2, paths: [{ id: 'b', parentId: 'a', code: '둘째' }] });
  assert.equal(state.view.paths[0].code, '첫째');
  assert.equal(state.live.paths[0].code, '둘째');
  state = reduce(state, { type: 'resume' });
  assert.equal(state.view.paths[0].code, '둘째');
});

test('기록 선택은 실시간 상태를 덮지 않고 재생 복귀가 가능하다', () => {
  let state = reduce(initialState(), { type: 'frontier', step: 1, paths: [{ id: 'a' }] });
  state = reduce(state, { type: 'frontier', step: 2, paths: [{ id: 'b' }] });
  state = reduce(state, { type: 'select', index: 0 });
  assert.equal(state.view.step, 1);
  assert.equal(state.live.step, 2);
  state = reduce(state, { type: 'resume' });
  assert.equal(state.view.step, 2);
});

test('suite 기록은 각 문제 시작부터 다음 문제 시작 전까지 분리한다', async () => {
  const { groupProblems } = await import('./state.mjs');
  const groups = groupProblems([{kind:'problem_started',data:{id:'a',label:'A'}},{kind:'search',data:{step:1}},{kind:'problem_started',data:{id:'b',label:'B'}},{kind:'search',data:{step:2}},{kind:'run_finished',data:{}}]);
  assert.deepEqual(groups.map(g => [g.problem.id, g.events.length]), [['a',2],['b',3]]);
});

test('연속 수신은 다음 프레임에서 한 번만 렌더한다', async () => {
  const { createRenderScheduler } = await import('./state.mjs');
  let callback, calls = 0;
  const schedule = createRenderScheduler(() => calls++, fn => { callback = fn; });
  schedule(); schedule(); schedule();
  assert.equal(calls, 0);
  callback();
  assert.equal(calls, 1);
  schedule(); callback();
  assert.equal(calls, 2);
});

test('동일 후보의 평가와 유지 상태는 한 행으로 갱신한다', async () => {
  const { updateCandidates } = await import('./state.mjs');
  let rows = updateCandidates([], {id:'a', step:1, reason:'available', lexeme:'x'});
  rows = updateCandidates(rows, {id:'a', step:1, reason:'ranked', score:0.8});
  rows = updateCandidates(rows, {id:'a', step:1, reason:'active'});
  assert.equal(rows.length, 1);
  assert.equal(rows[0].reason, 'active');
  assert.equal(rows[0].score, 0.8);
  assert.equal(rows[0].lexeme, 'x');
});

test('문제별 결과는 실행 종료와 별도로 한국어 상태를 제공한다', async () => {
  const { resultLabel } = await import('./state.mjs');
  assert.equal(resultLabel('success'), '성공');
  assert.equal(resultLabel('budget_exhausted'), '예산 소진');
  assert.equal(resultLabel('candidate_limit'), '후보 소진');
  assert.equal(resultLabel('heldout_failed'), '최종 평가 실패');
});

test('새로고침은 진행 중인 실행 하나만 다시 연결한다', async () => {
  const { findRunningRun } = await import('./state.mjs');
  const rows = [{run_id:'done',status:'completed'}, {run_id:'active',status:'running'}];
  assert.equal(findRunningRun(rows, null), 'active');
  assert.equal(findRunningRun(rows, 'existing'), null);
  assert.equal(findRunningRun([{run_id:'cancel',status:'cancelling'}], null), 'cancel');
});

test('저장 오류는 이벤트 종류와 종료 상태 모두에서 감지한다', async () => {
  const { isStorageError } = await import('./state.mjs');
  assert.equal(isStorageError('storage_error', {}), true);
  assert.equal(isStorageError('run_finished', {status:'storage_error'}), true);
  assert.equal(isStorageError('run_finished', {status:'completed'}), false);
});

test('snapshot 복구는 같은 문제 기록을 유지하고 새 문제에서는 경계를 초기화한다', async () => {
  const { recoverSnapshot } = await import('./state.mjs');
  let state = reduce(initialState(), {type:'frontier', step:1, paths:[{id:'old'}]});
  let recovered = recoverSnapshot(state, 'p1', {current:{id:'p1'}, step:2, frontier:[{id:'x', code:'x'}]});
  assert.equal(recovered.state.history.length, 2);
  assert.equal(recovered.nodes.get('x').code, 'x');
  recovered = recoverSnapshot(recovered.state, 'p1', {current:{id:'p2'}, step:1, frontier:[{id:'y', code:'y'}]});
  assert.equal(recovered.state.history.length, 1);
  assert.equal(recovered.state.live.paths[0].id, 'y');
});

test('선언 차단은 frontier를 바꾸지 않고 일시정지 및 기록 선택을 존중한다', async () => {
  const { rejectionLabel } = await import('./state.mjs');
  let state = reduce(initialState(), {type:'frontier', step:1, paths:[{id:'kept',code:'x'}]});
  state = reduce(state, {type:'pause'});
  state = reduce(state, {type:'declaration_rejected', rejection_reason:'parameter_count'});
  assert.equal(state.view.rejection, undefined);
  assert.equal(state.live.paths[0].id, 'kept');
  state = reduce(state, {type:'resume'});
  assert.equal(rejectionLabel(state.view.rejection), '매개변수 개수가 요구사항과 다릅니다.');
  state = reduce(state, {type:'frontier', step:2, paths:[{id:'next'}]});
  state = reduce(state, {type:'select', index:0});
  assert.equal(state.view.rejection, 'parameter_count');
  assert.equal(rejectionLabel('<unsafe>'), '선언 형식이 요구사항과 맞지 않습니다.');
});
