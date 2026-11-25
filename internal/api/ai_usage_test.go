package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"server-aibot/internal/store"
)

type dailyUsageResponse struct {
	Items      []store.AIUsageDay `json:"items"`
	Days       int                `json:"days"`
	Timezone   string             `json:"timezone"`
	FromDay    string             `json:"fromDay"`
	ThroughDay string             `json:"throughDay"`
}

func TestAIDailyUsageSnapshotsUTCAndBounds(t *testing.T) {
	e := newTestEnv(t)
	conv := seedAI(t, e, "http://127.0.0.1:1/v1")
	ctx := context.Background()
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	price := `{"inputPerMillion":2,"outputPerMillion":4,"currency":"USD"}`
	zeroPrice := `{"inputPerMillion":0,"outputPerMillion":0,"currency":"USD"}`
	missingPrice := `{"inputPerMillion":null,"outputPerMillion":4,"currency":"USD"}`
	ten, zero := 10, 0
	cost, free := 0.00002, 0.0
	seed := func(model, at, status, snapshot string, tokens *int, amount *float64, estimate int) {
		t.Helper()
		m := store.AIMessage{Role: "assistant", Status: status, EstimatedInput: estimate, ServedModelID: model, TokensIn: tokens, TokensOut: tokens, Cost: amount, PriceSnapshot: snapshot}
		if tokens != nil {
			m.TokensOut = &zero
		}
		id, err := e.store.AddAIMessage(ctx, conv, m)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = e.store.DB().Exec(`UPDATE ai_messages SET created_at=? WHERE id=?`, at, id); err != nil {
			t.Fatal(err)
		}
	}
	stamp := today.Add(time.Hour).Format(time.RFC3339)
	// Historical configured model differs from the model that actually served.
	seed("actual", stamp, "completed", price, &ten, &cost, 100)
	seed("actual", stamp, "error", missingPrice, &ten, nil, 100)
	seed("actual", stamp, "cancelled", price, nil, nil, 100)
	seed("free", stamp, "completed", zeroPrice, &zero, &free, 100)
	seed("", stamp, "interrupted", "", nil, nil, 100)
	// Explicit offset crosses the UTC date boundary into today.
	seed("offset", today.AddDate(0, 0, -1).Format("2006-01-02")+"T23:30:00-02:00", "completed", price, &ten, &cost, 100)
	seed("actual", today.Add(-time.Second).Format(time.RFC3339), "completed", price, &ten, &cost, 100)
	seed("outside", today.AddDate(0, 0, -30).Format(time.RFC3339), "completed", price, &ten, &cost, 100)
	seed("future", today.AddDate(0, 0, 1).Format(time.RFC3339), "completed", price, &ten, &cost, 100)
	seed("approval-result", stamp, "completed", "", nil, nil, 0)
	// Price changes must never alter the persisted cost or price snapshot.
	var model store.AIModel
	if err := e.store.GetAIConfig(ctx, "models", "ai_model", &model); err != nil {
		t.Fatal(err)
	}
	newPrice := 999.0
	model.InputPrice = &newPrice
	model.OutputPrice = &newPrice
	if err := e.store.SaveAIConfig(ctx, "models", model.ID, "", model.ProviderID, model, false); err != nil {
		t.Fatal(err)
	}
	res := e.do(t, "GET", "/api/v1/ai/usage/daily?days=1", nil, false)
	if res.StatusCode != 200 {
		t.Fatalf("daily status=%d", res.StatusCode)
	}
	result := decode[dailyUsageResponse](t, res)
	if result.Days != 1 || result.Timezone != "UTC" || result.FromDay != today.Format("2006-01-02") || result.ThroughDay != result.FromDay || len(result.Items) != 4 {
		t.Fatalf("UTC daily=%+v", result)
	}
	byModel := map[string]store.AIUsageDay{}
	for _, item := range result.Items {
		byModel[item.ModelID] = item
		if item.Day != result.FromDay {
			t.Fatalf("wrong UTC day: %+v", item)
		}
	}
	actual := byModel["actual"]
	if actual.Requests != 3 || actual.ReportedRequests != 2 || actual.UnknownUsageRequests != 1 || actual.PricedRequests != 1 || actual.UnknownCostRequests != 2 || actual.UnknownPriceRequests != 1 || actual.TokensIn == nil || *actual.TokensIn != 20 || actual.TokensOut == nil || *actual.TokensOut != 0 || actual.EstimatedCost == nil || math.Abs(*actual.EstimatedCost-cost) > 1e-12 {
		t.Fatalf("mixed known/unknown usage: %+v", actual)
	}
	freeRow := byModel["free"]
	if freeRow.EstimatedCost == nil || *freeRow.EstimatedCost != 0 || freeRow.UnknownCostRequests != 0 || freeRow.UnknownPriceRequests != 0 || freeRow.TokensIn == nil || *freeRow.TokensIn != 0 {
		t.Fatalf("zero treated as unknown: %+v", freeRow)
	}
	unknown := byModel[""]
	if unknown.Requests != 1 || unknown.EstimatedCost != nil || unknown.TokensIn != nil || unknown.TokensOut != nil || unknown.UnknownCostRequests != 1 || unknown.UnknownPriceRequests != 1 {
		t.Fatalf("unknown treated as zero or requested model: %+v", unknown)
	}
	if byModel["offset"].Requests != 1 {
		t.Fatal("offset timestamp not grouped by UTC")
	}
	// Default window is bounded and excludes tomorrow and old history.
	res = e.do(t, "GET", "/api/v1/ai/usage/daily", nil, false)
	defaults := decode[dailyUsageResponse](t, res)
	if defaults.Days != 30 || len(defaults.Items) != 5 {
		t.Fatalf("default window=%+v", defaults)
	}
	for _, item := range defaults.Items {
		if item.ModelID == "outside" || item.ModelID == "future" || item.ModelID == "approval-result" {
			t.Fatal("unrelated records entered usage")
		}
	}
	messages, err := e.store.AIMessages(ctx, conv)
	if err != nil {
		t.Fatal(err)
	}
	if messages[0].PriceSnapshot != price || messages[0].Cost == nil || *messages[0].Cost != cost {
		t.Fatal("historical snapshot changed")
	}
	for _, query := range []string{"0", "367", "-1", "abc", "1.5", "", "1&days=2"} {
		res = e.do(t, "GET", "/api/v1/ai/usage/daily?days="+query, nil, false)
		res.Body.Close()
		if res.StatusCode != 400 {
			t.Fatalf("invalid days %q returned %d", query, res.StatusCode)
		}
	}
	res = e.do(t, "GET", "/api/v1/ai/usage/daily?days=366", nil, false)
	if res.StatusCode != 200 {
		t.Fatalf("maximum days=%d", res.StatusCode)
	}
	res.Body.Close()
	rows, err := e.store.AIUsageDaily(ctx, "different-owner", 30, now)
	if err != nil || len(rows) != 0 {
		t.Fatal("daily usage exposed another owner's rows")
	}
	for _, days := range []int{0, 367} {
		if _, err = e.store.AIUsageDaily(ctx, "different-owner", days, now); err == nil {
			t.Fatal("store accepted unbounded days")
		}
	}
	anonymous, err := http.Get(e.srv.URL + "/api/v1/ai/usage/daily")
	if err != nil {
		t.Fatal(err)
	}
	anonymous.Body.Close()
	if anonymous.StatusCode != 401 {
		t.Fatalf("anonymous usage=%d", anonymous.StatusCode)
	}
}

func TestAIDailyUsageNoOutputAndMissingPrices(t *testing.T) {
	for _, kind := range []string{"priced-zero-output", "missing-prices", "zero-prices", "no-usage-error"} {
		t.Run(kind, func(t *testing.T) {
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if kind == "no-usage-error" {
					w.WriteHeader(503)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":0}}\n\ndata: [DONE]\n\n")
			}))
			defer provider.Close()
			e := newTestEnv(t)
			conv := seedAI(t, e, provider.URL)
			ctx := context.Background()
			var model store.AIModel
			if err := e.store.GetAIConfig(ctx, "models", "ai_model", &model); err != nil {
				t.Fatal(err)
			}
			rate := 2.0
			if kind == "zero-prices" {
				rate = 0
			}
			if kind != "missing-prices" {
				model.InputPrice = &rate
				model.OutputPrice = &rate
			}
			if err := e.store.SaveAIConfig(ctx, "models", model.ID, "", model.ProviderID, model, false); err != nil {
				t.Fatal(err)
			}
			res := e.do(t, "POST", "/api/v1/ai/conversations/"+conv+"/messages", map[string]string{"content": "fixture"}, true)
			if res.StatusCode != 200 {
				t.Fatalf("stream=%d", res.StatusCode)
			}
			io.Copy(io.Discard, res.Body)
			res.Body.Close()
			messages, err := e.store.AIMessages(ctx, conv)
			if err != nil || len(messages) != 2 {
				t.Fatalf("messages=%+v err=%v", messages, err)
			}
			saved := messages[1]
			if saved.Content != "" || saved.ServedModelID != model.ID {
				t.Fatalf("no-output model persistence=%+v", saved)
			}
			var snapshot struct {
				Input  *float64 `json:"inputPerMillion"`
				Output *float64 `json:"outputPerMillion"`
			}
			if err = json.Unmarshal([]byte(saved.PriceSnapshot), &snapshot); err != nil {
				t.Fatal(err)
			}
			res = e.do(t, "GET", "/api/v1/ai/usage/daily?days=1", nil, false)
			result := decode[dailyUsageResponse](t, res)
			if len(result.Items) != 1 || result.Items[0].Requests != 1 {
				t.Fatalf("no-output request excluded: %+v", result)
			}
			row := result.Items[0]
			switch kind {
			case "missing-prices":
				if snapshot.Input != nil || snapshot.Output != nil || row.EstimatedCost != nil || row.UnknownPriceRequests != 1 || row.ReportedRequests != 1 {
					t.Fatalf("missing prices=%+v", row)
				}
			case "no-usage-error":
				if snapshot.Input == nil || *snapshot.Input != 2 || row.EstimatedCost != nil || row.UnknownPriceRequests != 0 || row.UnknownUsageRequests != 1 || saved.Status != "error" {
					t.Fatalf("failed request=%+v saved=%+v", row, saved)
				}
			default:
				expected := 10 * rate / 1000000
				if row.EstimatedCost == nil || math.Abs(*row.EstimatedCost-expected) > 1e-12 || row.ReportedRequests != 1 || row.UnknownPriceRequests != 0 || row.TokensOut == nil || *row.TokensOut != 0 {
					t.Fatalf("priced no-output=%+v", row)
				}
			}
		})
	}
}
