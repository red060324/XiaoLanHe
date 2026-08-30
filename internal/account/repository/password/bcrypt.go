package password

import "golang.org/x/crypto/bcrypt"

type Bcrypt struct{ Cost int }

func (b Bcrypt) Hash(password string) (string, error) {
	cost := b.Cost
	if cost == 0 {
		cost = 12
	}
	value, err := bcrypt.GenerateFromPassword([]byte(password), cost)
	return string(value), err
}

func (b Bcrypt) Compare(hash, password string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
}
