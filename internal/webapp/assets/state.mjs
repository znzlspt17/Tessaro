// 2026-11-05: 화면 탐색과 실행 수신을 분리해 일시정지해도 최신 frontier를 보존한다.
export const initialState = () => ({ live: null, view: null, history: [], paused: false });
export function reduce(state, event) {
  switch (event.type) {
    case 'frontier': {
      const snapshot = { ...event };
      return { ...state, live: snapshot, view: state.paused ? state.view : snapshot, history: [...state.history, snapshot] };
    }
    case 'declaration_rejected': {
      const live = {...(state.live || {paths:[], step:event.step}), rejection:event.rejection_reason || 'unknown'};
      const history = state.history.length ? [...state.history.slice(0, -1), live] : state.history;
      return {...state, live, history, view:state.paused ? state.view : live};
    }
    case 'pause': return { ...state, paused: true };
    case 'select': return state.history[event.index] ? { ...state, paused: true, view: state.history[event.index] } : state;
    case 'resume': return { ...state, paused: false, view: state.live };
    case 'reset': return initialState();
    default: return state;
  }
}

// 2026-11-05: suite trace의 문제 경계를 보존해 다른 문제의 이벤트가 섞이지 않게 한다.
export function groupProblems(events) {
  const groups = [];
  for (const event of events) {
    if (event.kind === 'problem_started') groups.push({problem: event.data, events: []});
    if (groups.length) groups[groups.length - 1].events.push(event);
  }
  return groups;
}

// 2026-11-05: 이벤트는 빠짐없이 반영하되 한 프레임의 중복 DOM 갱신은 합친다.
export function createRenderScheduler(render, requestFrame) {
  let pending = false;
  return () => {
    if (pending) return;
    pending = true;
    requestFrame(() => { pending = false; render(); });
  };
}

// 2026-11-05: 같은 후보의 평가 과정은 현재 상태 한 행으로 합치고 원본은 trace에 둔다.
export function updateCandidates(rows, event) {
  const current = event.reason === 'available' && rows.length && rows[0].step !== event.step ? [] : rows;
  const index = current.findIndex(row => row.id === event.id);
  if (index < 0) return [...current, event];
  return current.map((row, i) => i === index ? {...row, ...event} : row);
}

export function resultLabel(status) {
  return ({success:'성공', budget_exhausted:'예산 소진', candidate_limit:'후보 소진', no_solution:'해결 경로 없음', model_error:'모델 오류', cancelled:'취소', timeout:'시간 초과', heldout_failed:'최종 평가 실패', heldout_error:'최종 평가 오류', heldout_timeout:'최종 평가 시간 초과', input_error:'입력 오류', execution_error:'실행 오류'})[status] || '결과 미확인';
}

export function findRunningRun(runs, activeRun) {
  if (activeRun) return null;
  return runs.find(run => run.status === 'running' || run.status === 'cancelling')?.run_id || null;
}

export function isStorageError(kind, data) {
  return kind === 'storage_error' || data?.status === 'storage_error';
}

// 2026-11-05: 재접속은 snapshot만 복원해 누적 trace 크기와 무관하게 현재 경로를 표시한다.
export function recoverSnapshot(state, currentProblemId, snapshot, existingNodes = new Map()) {
  const problemId = snapshot.current?.id || null;
  const sameProblem = problemId !== null && problemId === currentProblemId;
  const nodes = sameProblem ? new Map(existingNodes) : new Map([['root', {code:''}]]);
  const paths = snapshot.frontier || [];
  for (const path of [...paths, ...(snapshot.candidates || [])]) {
    if (path.id && typeof path.code === 'string') nodes.set(path.id, {...nodes.get(path.id), ...path});
  }
  const base = sameProblem ? state : state.paused ? {...initialState(), paused:true, view:state.view} : initialState();
  const displayPaths = paths.map(path => ({...nodes.get(path.id), ...path}));
  const nextState = reduce(base, {type:'frontier', step:snapshot.step ?? 0, paths:displayPaths, candidates:snapshot.candidates || [], rejection:sameProblem ? state.live?.rejection : undefined});
  return {state:nextState, nodes, currentProblemId:problemId};
}

// 2026-11-05: 허용된 사유만 표시해 모델 후보 점수와 선언 검사를 구분한다.
export function rejectionLabel(reason) {
  const labels = {parameter_count:'매개변수 개수가 요구사항과 다릅니다.', parameter_name:'매개변수 이름이 요구사항과 다릅니다.', parameter_type:'매개변수 타입이 요구사항과 다릅니다.', variadic:'가변 매개변수가 허용되지 않습니다.', return_type:'반환 타입이 요구사항과 다릅니다.', missing_return_type:'필요한 반환 타입이 없습니다.', invalid_signature:'함수 선언 형식이 유효하지 않습니다.'};
  return Object.hasOwn(labels, reason) ? labels[reason] : '선언 형식이 요구사항과 맞지 않습니다.';
}
