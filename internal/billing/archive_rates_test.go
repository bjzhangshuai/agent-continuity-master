package billing_test

import (
	"github.com/agent-continuity/agent-continuity-master/internal/billing"
	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
	"testing"
)

func TestArchiveRatesExplicitAndFreeByDefault(t *testing.T) {
	book := billing.DemoPriceBook()
	if err := book.ValidateArchiveRates(); err != nil {
		t.Fatal(err)
	}
	for _, rates := range book.Currencies {
		_, amount, err := billing.Calculate([]protocol.UsageTotal{{Metric: "storage.archive.bytes", Quantity: 1024, Unit: "byte"}, {Metric: "storage.download.bytes", Quantity: 1024, Unit: "byte"}, {Metric: "storage.retained.byte_hours", Quantity: 2048, Unit: "byte_hour"}}, rates)
		if err != nil || amount != 0 {
			t.Fatal("unexpected default storage charge", amount, err)
		}
	}
	book.Currencies["CNY"] = book.Currencies["CNY"][:2]
	if book.ValidateArchiveRates() == nil {
		t.Fatal("unpriced archive meters accepted")
	}
}
