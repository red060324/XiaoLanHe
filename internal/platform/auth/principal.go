package auth

import "errors"

var ErrUnauthenticated = errors.New("unauthenticated")

type Role string

const (
	RoleUser  Role = "user"
	RoleAdmin Role = "admin"
)

type Principal struct {
	UserID      int64
	Username    string
	DisplayName string
	Role        Role
}

func (p Principal) IsAdmin() bool { return p.Role == RoleAdmin }
