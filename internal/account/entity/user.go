package entity

import "github.com/red060324/XiaoLanHe/internal/platform/auth"

type User struct {
	ID          int64
	Username    string
	DisplayName string
	Role        auth.Role
	Status      string
}

func (u User) Principal() auth.Principal {
	return auth.Principal{UserID: u.ID, Username: u.Username, DisplayName: u.DisplayName, Role: u.Role}
}
