package desktop

func RejectionLabel(reason string) string {
	labels := map[string]string{"parameter_count": "매개변수 개수가 요구사항과 다릅니다.", "parameter_name": "매개변수 이름이 요구사항과 다릅니다.", "parameter_type": "매개변수 타입이 요구사항과 다릅니다.", "variadic": "가변 매개변수가 허용되지 않습니다.", "return_type": "반환 타입이 요구사항과 다릅니다.", "missing_return_type": "필요한 반환 타입이 없습니다.", "invalid_signature": "함수 선언 형식이 유효하지 않습니다.", "unused_expression": "독립된 산술식은 문장으로 사용할 수 없습니다.", "branch_outside_loop": "반복문 밖의 break 또는 continue입니다.", "statement_limit": "문장 수 한도를 초과했습니다.", "nesting_limit": "중첩 한도를 초과했습니다."}
	if reason == "" {
		return ""
	}
	if reason == "non_callable" {
		return "함수로 호출할 수 없는 값을 호출했습니다."
	}
	if reason == "irrecoverable_expression_statement" {
		return "완성 불가능한 문장입니다."
	}
	if reason == "bool_order_comparison" {
		return "참·거짓 값에는 크기 비교를 할 수 없습니다."
	}
	if s, ok := labels[reason]; ok {
		return s
	}
	return "요구사항에 맞지 않는 항목이 있습니다."
}
