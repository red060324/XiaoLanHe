package order

import (
	"context"
	"errors"

	flashsale "github.com/red060324/XiaoLanHe/internal/flashsale/usecase"
	order "github.com/red060324/XiaoLanHe/internal/order/usecase"
)

type Service struct {
	orders *order.Service
}

func NewService(orders *order.Service) *Service { return &Service{orders: orders} }

func (s *Service) CreateFromFlashSale(ctx context.Context, command flashsale.OrderCommand) (flashsale.OrderResult, error) {
	result, err := s.orders.CreateFromFlashSale(ctx, order.FlashSaleCreateInput{
		RequestID: command.RequestID, UserID: command.UserID, Offer: command.Offer,
		SalePriceMinor: command.SalePriceMinor, PaymentExpiresAt: command.PaymentExpiresAt,
	})
	err = mapOrderError(err)
	return flashsale.OrderResult{Order: result.Order, Replayed: result.Replayed}, err
}

func mapOrderError(err error) error {
	if errors.Is(err, order.ErrAlreadyOwned) {
		return flashsale.ErrAlreadyOwned
	}
	if errors.Is(err, order.ErrInvalidInput) || errors.Is(err, order.ErrPriceUnavailable) || errors.Is(err, order.ErrIdempotencyConflict) {
		return flashsale.ErrOrderUnavailable
	}
	return err
}

var _ flashsale.Orders = (*Service)(nil)
