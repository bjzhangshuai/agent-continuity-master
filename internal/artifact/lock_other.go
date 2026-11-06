//go:build !linux && !darwin

package artifact

import (
	"context"
	"errors"
)

func (s *Service) LockContent(ctx context.Context, hash string) (func(), error) {
	return nil, errors.New("server shared staging requires Linux or macOS POSIX locks")
}
