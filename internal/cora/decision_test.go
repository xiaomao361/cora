package cora

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCoraExperiencePackGoldenCases(t *testing.T) {
	core, err := defaultCoraCore()
	if err != nil {
		t.Fatal(err)
	}
	pack := core.(*ruleCora).packs["gbjk-zhifu"]
	if pack.Version != "cora-gbjk-v0.1.1" {
		t.Fatalf("experience version=%q", pack.Version)
	}
	rules := pack.Rules
	if len(rules) != 131 {
		t.Fatalf("loaded %d Cora rules, want 131", len(rules))
	}

	tests := []struct {
		name     string
		event    Event
		decision string
		ruleID   string
		source   string
	}{
		{
			name: "database disconnect needs attention",
			event: Event{ProductLine: "gbjk-zhifu", Service: "gb-order",
				Logger:        "com.alibaba.druid.pool.DruidPooledStatement",
				ExceptionType: "com.mysql.cj.jdbc.exceptions.CommunicationsException",
				Message:       "CommunicationsException while checking pooled connection"},
			decision: DecisionAttention, ruleID: "at_01", source: "experience_pack",
		},
		{
			name: "expired client token is ignored",
			event: Event{ProductLine: "gbjk-zhifu", Service: "gb-gateway",
				Logger: "com.guanbai.RouterServiceImpl", ExceptionType: "TokenException",
				Message: "token expired", Stacktrace: "at com.guanbai.RouterServiceImpl.checkAccess(RouterServiceImpl.java:42)"},
			decision: DecisionIgnore, ruleID: "ig_03", source: "experience_pack",
		},
		{
			name: "confirmed normal subsidy validation wrapped by redisson is ignored",
			event: Event{ProductLine: "gbjk-zhifu", Service: "gb-order",
				Logger: "com.gbjk.common.redis.service.RedissonService", ExceptionType: "logback.ERROR",
				Message:     "分布式锁Runnable方法执行失败：",
				Stacktrace:  "at com.gbjk.common.redis.service.RedissonService.lockFun(RedissonService.java:118)",
				Breadcrumbs: []Breadcrumb{{Message: "事务日志切面回滚，异常信息：[特殊人群补助]未在投保信息中找到"}}},
			decision: DecisionIgnore, ruleID: "ig_63", source: "experience_pack",
		},
		{
			name: "confirmed normal subsidy validation wrapped by seata is ignored",
			event: Event{ProductLine: "gbjk-zhifu", Service: "gb-order",
				Logger:        "com.gbjk.common.security.handler.GlobalExceptionHandler",
				ExceptionType: "com.gbjk.common.core.exception.UtilException",
				Message:       "请求地址'/inner/cases/addClaimCasesV1',发生未知异常.",
				Stacktrace:    "at io.seata.tm.api.DefaultGlobalTransaction.rollback(DefaultGlobalTransaction.java:171)",
				Breadcrumbs:   []Breadcrumb{{Message: "异常信息：[特殊人群补助]未在投保信息中找到"}}},
			decision: DecisionIgnore, ruleID: "ig_63", source: "experience_pack",
		},
		{
			name: "other redisson execution failure still needs attention",
			event: Event{ProductLine: "gbjk-zhifu", Service: "gb-order",
				Logger: "com.gbjk.common.redis.service.RedissonService", ExceptionType: "logback.ERROR",
				Message:    "分布式锁Runnable方法执行失败：",
				Stacktrace: "at com.gbjk.common.redis.service.RedissonService.lockFun(RedissonService.java:118)"},
			decision: DecisionAttention, ruleID: "at_02", source: "experience_pack",
		},
		{
			name: "other seata exception still needs attention",
			event: Event{ProductLine: "gbjk-zhifu", Service: "gb-order",
				Logger:        "com.gbjk.common.security.handler.GlobalExceptionHandler",
				ExceptionType: "com.gbjk.common.core.exception.UtilException",
				Message:       "unknown failure",
				Stacktrace:    "at io.seata.tm.api.DefaultGlobalTransaction.rollback(DefaultGlobalTransaction.java:171)"},
			decision: DecisionAttention, ruleID: "at_03", source: "experience_pack",
		},
		{
			name: "unmatched Guanbai error stays observable",
			event: Event{ProductLine: "gbjk-zhifu", Service: "gb-order",
				Logger: "com.guanbai.NewFailure", ExceptionType: "NewFailure", Message: "new failure"},
			decision: DecisionObserve, ruleID: "cora.default.unmatched", source: "experience_pack",
		},
		{
			name: "same database text does not leak to another product line",
			event: Event{ProductLine: "qikang-zhifu", Service: "qk-order",
				Logger:        "com.alibaba.druid.pool.DruidPooledStatement",
				ExceptionType: "CommunicationsException", Message: "CommunicationsException"},
			decision: DecisionObserve, ruleID: "cora.default.untrained-product-line", source: "framework_default",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision, err := core.Decide(context.Background(), DecisionRequest{Event: test.event, FirstOccurrence: true})
			if err != nil {
				t.Fatal(err)
			}
			if decision.Decision != test.decision || decision.RuleID != test.ruleID || decision.Source != test.source {
				t.Fatalf("decision=%+v", decision)
			}
		})
	}
}

func TestStorePersistsCoraDecisionAndAttentionExcludesIgnore(t *testing.T) {
	store, err := OpenStore(t.TempDir() + "/cora.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	attention := Event{ProductLine: "gbjk-zhifu", Service: "gb-order",
		Logger: "DruidDataSource", ExceptionType: "CommunicationsException", Message: "CommunicationsException"}
	ignored := Event{ProductLine: "gbjk-zhifu", Service: "gb-gateway",
		Logger: "RouterServiceImpl", ExceptionType: "TokenException", Message: "expired",
		Stacktrace: "at com.guanbai.RouterServiceImpl.checkAccess(RouterServiceImpl.java:42)"}
	for _, event := range []Event{attention, ignored} {
		if err := store.Record(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}

	items, err := store.Attention(context.Background(), "gbjk-zhifu")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Decision != DecisionAttention || items[0].RuleID != "at_01" {
		t.Fatalf("attention items=%+v", items)
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/attention?product_line=gbjk-zhifu", nil)
	response := httptest.NewRecorder()
	Handler(store).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Attention []AttentionItem `json:"attention"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Attention) != 1 || body.Attention[0].Fingerprint != Fingerprint(attention) {
		t.Fatalf("body=%s", response.Body.String())
	}
}

func TestStorePassesOccurrenceContextAcrossCoraBoundary(t *testing.T) {
	core := &recordingCora{}
	store, err := OpenStoreWithCora(t.TempDir()+"/cora.db", core)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	event := Event{ProductLine: "line", Service: "api", ExceptionType: "Timeout"}
	if err := store.Record(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if err := store.Record(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if len(core.requests) != 2 || !core.requests[0].FirstOccurrence || core.requests[0].OccurrenceCount != 1 ||
		core.requests[1].FirstOccurrence || core.requests[1].OccurrenceCount != 2 {
		t.Fatalf("requests=%+v", core.requests)
	}
}

func TestCoraFailureDoesNotRollbackProblemFacts(t *testing.T) {
	store, err := OpenStoreWithCora(t.TempDir()+"/cora.db", failingCora{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	event := Event{ProductLine: "line", Service: "api", ExceptionType: "Timeout"}
	if err := store.Record(context.Background(), event); err != nil {
		t.Fatalf("record should survive Cora failure: %v", err)
	}
	problems, err := store.Problems(context.Background(), "line")
	if err != nil || len(problems) != 1 {
		t.Fatalf("problem facts missing: problems=%v err=%v", problems, err)
	}
	items, err := store.Attention(context.Background(), "line")
	if err != nil || len(items) != 1 || items[0].Decision != DecisionObserve ||
		items[0].RuleID != "cora.default.core-unavailable" {
		t.Fatalf("fallback decision missing: items=%v err=%v", items, err)
	}
}

type recordingCora struct{ requests []DecisionRequest }

func (c *recordingCora) Decide(_ context.Context, request DecisionRequest) (CoraDecision, error) {
	c.requests = append(c.requests, request)
	return CoraDecision{Decision: DecisionObserve, Category: "test", RuleID: "test",
		Reason: "test", Source: "test", DecidedAt: time.Now().UTC()}, nil
}

type failingCora struct{}

func (failingCora) Decide(context.Context, DecisionRequest) (CoraDecision, error) {
	return CoraDecision{}, errors.New("unavailable")
}
