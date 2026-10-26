package app

import "context"

func (s *Service) Integrity(ctx context.Context) (IntegrityView, error) {
	report, err := s.book.Integrity(ctx)
	if err != nil {
		return IntegrityView{}, err
	}
	return integrityView(report), nil
}
