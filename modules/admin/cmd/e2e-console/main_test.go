package main

import "testing"

func TestBusinessOKRequiresCompleteJSONWithZeroRetCode(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		raw  string
		want bool
	}{
		{"success", 200, `{"ret_info":{"code":0,"msg":"ok"},"x":1}`, true},
		{"spaced success", 200, `{"ret_info":{"code":0, "msg":""}}`, true},
		{"business error", 200, `{"ret_info":{"code":1,"msg":"bad"}}`, false},
		{"empty body", 200, ``, false},
		{"truncated", 200, `{"ret_info":{"code":0`, false},
		{"no ret_info", 200, `{"ok":true}`, false},
		{"http error", 403, `{"ret_info":{"code":0}}`, false},
	} {
		if got := businessOK(tc.code, tc.raw); got != tc.want {
			t.Errorf("%s: businessOK = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRetMessageNeverEchoesTokens(t *testing.T) {
	got := retMessage(`{"ret_info":{"msg":"用户名或密码错误"},"access_token":"secret-token","request_signing_key":"secret-key"}`)
	if got != "用户名或密码错误" {
		t.Fatalf("retMessage = %q", got)
	}
	if retMessage(`{"access_token":"secret-token"`) != "(响应不可解析)" {
		t.Fatal("不可解析响应不得回显内容")
	}
}
