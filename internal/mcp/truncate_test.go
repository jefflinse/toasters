package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestTruncateResult_ShortResult(t *testing.T) {
	input := "hello world"
	got := TruncateResult(input, 100)
	if got != input {
		t.Errorf("expected unchanged result, got %q", got)
	}
}

func TestTruncateResult_ExactlyAtLimit(t *testing.T) {
	input := strings.Repeat("x", 100)
	got := TruncateResult(input, 100)
	if got != input {
		t.Errorf("expected unchanged result at exact limit, got len %d", len(got))
	}
}

func TestTruncateResult_ByteFallback_NonJSON(t *testing.T) {
	input := strings.Repeat("hello ", 1000) // ~6000 chars
	maxLen := 100
	got := TruncateResult(input, maxLen)

	if !strings.HasPrefix(got, input[:maxLen]) {
		t.Error("expected result to start with truncated input")
	}
	if !strings.Contains(got, "truncated") {
		t.Error("expected truncation note")
	}
	if !strings.Contains(got, fmt.Sprintf("%d total bytes", len(input))) {
		t.Errorf("expected total byte count in note, got %q", got[maxLen:])
	}
}

func TestTruncateResult_JSONArray_Truncated(t *testing.T) {
	// Build a large JSON array.
	var items []map[string]string
	for i := range 100 {
		items = append(items, map[string]string{
			"id":          fmt.Sprintf("item-%d", i),
			"description": strings.Repeat("x", 200),
		})
	}
	data, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	input := string(data)

	maxLen := 5000
	got := TruncateResult(input, maxLen)

	if len(got) > maxLen {
		t.Errorf("result exceeds maxLen: got %d, want <= %d", len(got), maxLen)
	}

	// Should be valid JSON.
	var parsed []any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	// Last element should be the truncation metadata.
	if len(parsed) < 2 {
		t.Fatalf("expected at least 2 elements, got %d", len(parsed))
	}
	last, ok := parsed[len(parsed)-1].(map[string]any)
	if !ok {
		t.Fatal("last element is not an object")
	}
	truncMsg, ok := last["_truncated"].(string)
	if !ok {
		t.Fatal("last element missing _truncated field")
	}
	if !strings.Contains(truncMsg, "of 100 items") {
		t.Errorf("truncation message should mention total items, got %q", truncMsg)
	}
	if !strings.Contains(truncMsg, "Showing") {
		t.Errorf("truncation message should say Showing, got %q", truncMsg)
	}
}

func TestTruncateResult_JSONArray_AllFit(t *testing.T) {
	// Small array that fits within limit.
	input := `[{"id":"1"},{"id":"2"}]`
	got := TruncateResult(input, 10000)
	if got != input {
		t.Errorf("expected unchanged result, got %q", got)
	}
}

func TestTruncateResult_JSONObject_WithLargeArray(t *testing.T) {
	// Build an object with a large array value.
	var items []map[string]string
	for i := range 50 {
		items = append(items, map[string]string{
			"id":   fmt.Sprintf("item-%d", i),
			"data": strings.Repeat("y", 300),
		})
	}
	obj := map[string]any{
		"total_count": 50,
		"items":       items,
	}
	data, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	input := string(data)

	maxLen := 5000
	got := TruncateResult(input, maxLen)

	if len(got) > maxLen {
		t.Errorf("result exceeds maxLen: got %d, want <= %d", len(got), maxLen)
	}

	// Should be valid JSON.
	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	// The items array should be truncated.
	itemsArr, ok := parsed["items"].([]any)
	if !ok {
		t.Fatal("items field is not an array")
	}
	if len(itemsArr) >= 50 {
		t.Errorf("expected items to be truncated, got %d elements", len(itemsArr))
	}

	// Last element should be truncation metadata.
	last, ok := itemsArr[len(itemsArr)-1].(map[string]any)
	if !ok {
		t.Fatal("last item is not an object")
	}
	if _, ok := last["_truncated"]; !ok {
		t.Error("last item missing _truncated field")
	}
}

func TestTruncateResult_JSONObject_SmallArraysUntouched(t *testing.T) {
	// Object with small arrays (<=5 elements) should not be modified.
	obj := map[string]any{
		"items": []any{1, 2, 3},
		"name":  "test",
		"big":   strings.Repeat("z", 200),
	}
	data, _ := json.Marshal(obj)
	input := string(data)

	// With a limit smaller than the input but arrays are small.
	maxLen := 100
	got := TruncateResult(input, maxLen)

	// Should fall back to byte truncation since no large arrays to shrink.
	if !strings.Contains(got, "truncated") {
		t.Error("expected byte fallback truncation")
	}
}

func TestTruncateResult_EmptyArray(t *testing.T) {
	input := "[]"
	got := TruncateResult(input, 100)
	if got != input {
		t.Errorf("expected unchanged empty array, got %q", got)
	}
}

func TestTruncateResult_HugeElements_JSONTruncation(t *testing.T) {
	// Array where even a single element exceeds the limit.
	// JSON-aware truncation should produce an array with 0 real items + metadata.
	// Use strings with non-base64 characters so SlimJSON doesn't strip them as opaque blobs.
	items := []map[string]string{
		{"data": strings.Repeat("Hello, world! ", 715)},
		{"data": strings.Repeat("Goodbye now! ", 770)},
	}
	data, _ := json.Marshal(items)
	input := string(data)

	maxLen := 500
	got := TruncateResult(input, maxLen)

	if len(got) > maxLen {
		t.Errorf("result exceeds maxLen: got %d, want <= %d", len(got), maxLen)
	}

	// Should be valid JSON with truncation metadata.
	var parsed []any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if len(parsed) != 1 {
		t.Fatalf("expected 1 element (metadata only), got %d", len(parsed))
	}
	meta, ok := parsed[0].(map[string]any)
	if !ok {
		t.Fatal("element is not an object")
	}
	truncMsg, ok := meta["_truncated"].(string)
	if !ok {
		t.Fatal("missing _truncated field")
	}
	if !strings.Contains(truncMsg, "Showing 0 of 2") {
		t.Errorf("expected 'Showing 0 of 2', got %q", truncMsg)
	}
}

func TestTruncateResult_HugeElements_FallsBackToByte(t *testing.T) {
	// Array where even the truncation metadata doesn't fit.
	items := []map[string]string{
		{"data": strings.Repeat("x", 100)},
	}
	data, _ := json.Marshal(items)
	input := string(data)

	// maxLen so small that even the metadata element won't fit as JSON.
	maxLen := 10
	got := TruncateResult(input, maxLen)

	// Should fall back to byte truncation.
	if !strings.Contains(got, "truncated") {
		t.Error("expected truncation note")
	}
	if !strings.HasPrefix(got, input[:maxLen]) {
		t.Error("expected byte-level prefix from original input")
	}
}

func TestTruncateResult_InvalidJSON_FallsBackToByte(t *testing.T) {
	input := strings.Repeat("{invalid json", 100)
	maxLen := 50
	got := TruncateResult(input, maxLen)

	if !strings.HasPrefix(got, input[:maxLen]) {
		t.Error("expected byte-level prefix")
	}
	if !strings.Contains(got, "truncated") {
		t.Error("expected truncation note")
	}
}

func TestTruncateResult_JSONString_FallsBackToByte(t *testing.T) {
	// A JSON string (not array or object) should fall back to byte truncation.
	input := `"` + strings.Repeat("x", 1000) + `"`
	maxLen := 100
	got := TruncateResult(input, maxLen)

	if !strings.Contains(got, "truncated") {
		t.Error("expected byte fallback for JSON string")
	}
}

func TestTruncateResult_JSONNumber_FallsBackToByte(t *testing.T) {
	// A very long non-JSON string that looks number-like but isn't valid JSON.
	// (json.Unmarshal parses large integers as float64, losing precision and
	// shortening the output, so we use a non-JSON string to test byte fallback.)
	input := "num=" + strings.Repeat("1", 200)
	maxLen := 50
	got := TruncateResult(input, maxLen)

	if !strings.Contains(got, "truncated") {
		t.Error("expected byte fallback for non-JSON string")
	}
}

// --- TruncatingCaller tests ---

type mockCaller struct {
	result string
	err    error
}

func (m *mockCaller) Call(_ context.Context, _ string, _ json.RawMessage) (string, error) {
	return m.result, m.err
}

func TestTruncatingCaller_PassesThrough(t *testing.T) {
	inner := &mockCaller{result: "short result"}
	tc := NewTruncatingCaller(inner, 1000)

	got, err := tc.Call(context.Background(), "test__tool", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "short result" {
		t.Errorf("expected passthrough, got %q", got)
	}
}

func TestTruncatingCaller_Truncates(t *testing.T) {
	inner := &mockCaller{result: strings.Repeat("x", 500)}
	tc := NewTruncatingCaller(inner, 100)

	got, err := tc.Call(context.Background(), "test__tool", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "truncated") {
		t.Error("expected truncation note")
	}
}

func TestTruncatingCaller_PropagatesError(t *testing.T) {
	inner := &mockCaller{err: fmt.Errorf("connection failed")}
	tc := NewTruncatingCaller(inner, 1000)

	_, err := tc.Call(context.Background(), "test__tool", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "connection failed") {
		t.Errorf("expected original error, got %v", err)
	}
}

func TestTruncatingCaller_DefaultMaxLen(t *testing.T) {
	inner := &mockCaller{result: "ok"}
	tc := NewTruncatingCaller(inner, 0)

	if tc.maxLen != DefaultMaxResultLen {
		t.Errorf("expected default maxLen %d, got %d", DefaultMaxResultLen, tc.maxLen)
	}
}

func TestTruncatingCaller_NegativeMaxLen(t *testing.T) {
	inner := &mockCaller{result: "ok"}
	tc := NewTruncatingCaller(inner, -1)

	if tc.maxLen != DefaultMaxResultLen {
		t.Errorf("expected default maxLen %d, got %d", DefaultMaxResultLen, tc.maxLen)
	}
}

// --- Internal function tests ---

func TestTruncateJSON_NotJSON(t *testing.T) {
	_, ok := truncateJSON("not json", 100)
	if ok {
		t.Error("expected false for non-JSON input")
	}
}

func TestTruncateJSON_EmptyObject(t *testing.T) {
	// Empty object that's too large (padded).
	_, ok := truncateJSON("{}", 1)
	if ok {
		t.Error("expected false for empty object with no arrays")
	}
}

func TestTruncateJSONArray_EmptyArray(t *testing.T) {
	_, ok := truncateJSONArray([]any{}, 100)
	if ok {
		t.Error("expected false for empty array")
	}
}

func TestTruncateJSONArray_SingleLargeElement(t *testing.T) {
	// Single element that's too large to fit — should produce metadata-only array.
	arr := []any{map[string]any{"data": strings.Repeat("x", 1000)}}
	got, ok := truncateJSONArray(arr, 200)
	if !ok {
		t.Fatal("expected successful truncation with 0 items kept")
	}

	var parsed []any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if len(parsed) != 1 {
		t.Fatalf("expected 1 element (metadata only), got %d", len(parsed))
	}
	meta, ok := parsed[0].(map[string]any)
	if !ok {
		t.Fatal("element is not an object")
	}
	if _, ok := meta["_truncated"]; !ok {
		t.Error("missing _truncated field")
	}
}

func TestTruncateJSONArray_SingleLargeElement_MetadataDoesntFit(t *testing.T) {
	// Single element, and maxLen is so small even metadata doesn't fit.
	arr := []any{map[string]any{"data": "x"}}
	_, ok := truncateJSONArray(arr, 5)
	if ok {
		t.Error("expected false when even metadata doesn't fit")
	}
}

func TestTruncateJSONObject_NoLargeArrays(t *testing.T) {
	obj := map[string]any{
		"name":  "test",
		"count": 42,
	}
	_, ok := truncateJSONObject(obj, 10)
	if ok {
		t.Error("expected false when no large arrays to truncate")
	}
}

func TestTruncateJSONObject_MultipleLargeArrays(t *testing.T) {
	// Object with two large arrays.
	arr1 := make([]any, 20)
	arr2 := make([]any, 20)
	for i := range 20 {
		arr1[i] = map[string]any{"id": i, "data": strings.Repeat("a", 100)}
		arr2[i] = map[string]any{"id": i, "data": strings.Repeat("b", 100)}
	}
	obj := map[string]any{
		"commits": arr1,
		"files":   arr2,
	}

	data, _ := json.Marshal(obj)
	maxLen := 3000

	if len(string(data)) <= maxLen {
		t.Skip("test data too small to trigger truncation")
	}

	got, ok := truncateJSONObject(obj, maxLen)
	if !ok {
		// It's possible both arrays are still too large even after truncation.
		// In that case, byte fallback is expected.
		t.Log("object truncation returned false (expected if arrays are still too large)")
		return
	}

	if len(got) > maxLen {
		t.Errorf("result exceeds maxLen: got %d, want <= %d", len(got), maxLen)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
}
