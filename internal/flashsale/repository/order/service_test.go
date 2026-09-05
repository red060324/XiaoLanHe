package order

import (
	"context"
	"errors"
	"testing"

	flashsale "github.com/red060324/XiaoLanHe/internal/flashsale/usecase"
	orderusecase "github.com/red060324/XiaoLanHe/internal/order/usecase"
)

func TestCreateFromFlashSaleMapsPermanentOrderErrors(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want error
	}{
		{name: "already owned", err: orderusecase.ErrAlreadyOwned, want: flashsale.ErrAlreadyOwned},
		{name: "price unavailable", err: orderusecase.ErrPriceUnavailable, want: flashsale.ErrOrderUnavailable},
		{name: "invalid frozen order", err: orderusecase.ErrInvalidInput, want: flashsale.ErrOrderUnavailable},
		{name: "conflicting replay", err: orderusecase.ErrIdempotencyConflict, want: flashsale.ErrOrderUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := mapOrderError(test.err); !errors.Is(got, test.want) {
				t.Fatalf("mapped error=%v want=%v", got, test.want)
			}
		})
	}
	if got := mapOrderError(context.Canceled); !errors.Is(got, context.Canceled) {
		t.Fatalf("transient error=%v", got)
	}
}
