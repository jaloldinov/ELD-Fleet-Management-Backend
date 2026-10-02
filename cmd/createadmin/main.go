// Command createadmin creates a platform administrator.
//
// This is how the very first admin is created. It is a CLI on purpose: an
// HTTP endpoint for "create an admin" would be a security hole.
//
// Usage:
//
//	go run ./cmd/createadmin -email=admin@example.com -first-name=Ada -last-name=Admin
//
// The password is read from the terminal without echo. For scripts/CI, set
// ADMIN_PASSWORD instead. It is never a flag: flags end up in shell history
// and are visible to other users in `ps`.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"maps"
	"os"
	"slices"
	"time"

	"golang.org/x/term"

	"github.com/jaloldinov/ELD-learning/internal/auth"
	"github.com/jaloldinov/ELD-learning/internal/config"
	"github.com/jaloldinov/ELD-learning/internal/database"
	"github.com/jaloldinov/ELD-learning/internal/model"
	"github.com/jaloldinov/ELD-learning/internal/repository"
	"github.com/jaloldinov/ELD-learning/internal/service"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	email := flag.String("email", "", "admin email (required)")
	firstName := flag.String("first-name", "", "first name (required)")
	lastName := flag.String("last-name", "", "last name (required)")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	password, err := readPassword()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := database.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	// Same layers as the API: the CLI is just another "front door" to the
	// service, so validation and hashing rules are identical.
	users := service.NewUserService(
		repository.NewUserRepository(pool),
		auth.NewBcryptHasher(auth.DefaultBcryptCost),
	)

	u, err := users.Create(ctx, service.CreateUserInput{
		CompanyID: nil, // platform user
		Email:     *email,
		Password:  password,
		FirstName: *firstName,
		LastName:  *lastName,
		Roles:     []string{model.RolePlatformAdmin},
	})
	if err != nil {
		var vErr *model.ValidationError
		if errors.As(err, &vErr) {
			// Map iteration order is random in Go; sort for stable output.
			for _, field := range slices.Sorted(maps.Keys(vErr.Fields)) {
				fmt.Fprintf(os.Stderr, "  %s: %s\n", field, vErr.Fields[field])
			}
			return errors.New("invalid input")
		}
		return err
	}

	fmt.Printf("Platform admin created: %s (id %s)\n", *u.Email, u.ID)
	return nil
}

// readPassword takes ADMIN_PASSWORD if set, otherwise asks twice on the
// terminal without echoing the characters.
func readPassword() (string, error) {
	if pw := os.Getenv("ADMIN_PASSWORD"); pw != "" {
		return pw, nil
	}

	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", errors.New("stdin is not a terminal: set ADMIN_PASSWORD")
	}

	fmt.Fprint(os.Stderr, "Password: ")
	first, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}

	fmt.Fprint(os.Stderr, "Repeat password: ")
	second, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}

	if string(first) != string(second) {
		return "", errors.New("passwords do not match")
	}
	return string(first), nil
}
