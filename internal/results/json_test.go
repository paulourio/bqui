package results

import (
	"encoding/json"
	"testing"
)

func TestIndentRowJSON(t *testing.T) {
	row := order(1001, "Ana", nil, item("A-1", 2, "x"))
	got := string(IndentRowJSON(ordersFields(), row))
	want := `{
  "order_id": "1001",
  "customer": {
    "name": "Ana",
    "tier": null
  },
  "items": [
    {
      "sku": "A-1",
      "qty": "2",
      "tags": [
        "x"
      ]
    }
  ]
}`

	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestAppendRowJSONMatchesConsoleEncoding(t *testing.T) {
	row := order(1001, "Ana", nil, item("A-1", 2, "x"), item("B-7", 1))
	got := string(AppendRowJSON(nil, ordersFields(), row))
	want := `{"order_id":"1001","customer":{"name":"Ana","tier":null},"items":[{"sku":"A-1","qty":"2","tags":["x"]},{"sku":"B-7","qty":"1","tags":[]}]}`

	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}

	if !json.Valid([]byte(got)) {
		t.Fatal("invalid JSON")
	}
}
