package auth

import "errors"

var (
	ErrInvalidToken  = errors.New("invalid token")
	ErrTokenExpired  = errors.New("token expired")
	ErrUserNotFound  = errors.New("user not found")
)

type User struct {
	ID    string
	Name  string
	Email string
	Roles []string
}

func ValidateToken(token string) (*User, error) {
	if token == "" {
		return nil, ErrInvalidToken
	}
	// TODO: implement JWT validation
	return &User{ID: "default", Name: "Anonymous"}, nil
}

func HasRole(user *User, role string) bool {
	for _, r := range user.Roles {
		if r == role {
			return true
		}
	}
	return false
}
