package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	pgxtransactor "github.com/nordew/pgx-transactor"
)

// UserRepository is an example storage implementation using Squirrel
type UserRepository struct {
	pgxtransactor.Storage
	squirrelHelper *pgxtransactor.SquirrelHelper
}

// NewUserRepository creates a new UserRepository
func NewUserRepository(pool *pgxpool.Pool) *UserRepository {
	return &UserRepository{
		Storage:        pgxtransactor.NewBaseStorage(pool),
		squirrelHelper: pgxtransactor.NewSquirrelHelper(),
	}
}

// User represents a user entity
type User struct {
	ID        int
	Name      string
	Email     string
	CreatedAt time.Time
}

// CreateUser creates a new user using Squirrel
func (r *UserRepository) CreateUser(ctx context.Context, user User) error {
	insertBuilder := r.Builder().
		Insert("users").
		Columns("name", "email", "created_at").
		Values(user.Name, user.Email, time.Now()).
		Suffix("RETURNING id")

	// Use squirrelHelper to execute the query
	row := r.squirrelHelper.QueryRow(ctx, r.GetQuerier(), insertBuilder)
	return row.Scan(&user.ID)
}

// GetUserByID retrieves a user by ID using Squirrel
func (r *UserRepository) GetUserByID(ctx context.Context, id int) (User, error) {
	var user User

	selectBuilder := r.Builder().
		Select("id", "name", "email", "created_at").
		From("users").
		Where("id = ?", id)

	row := r.squirrelHelper.QueryRow(ctx, r.GetQuerier(), selectBuilder)
	err := row.Scan(&user.ID, &user.Name, &user.Email, &user.CreatedAt)
	return user, err
}

// UpdateUser updates a user using Squirrel
func (r *UserRepository) UpdateUser(ctx context.Context, user User) error {
	updateBuilder := r.Builder().
		Update("users").
		Set("name", user.Name).
		Set("email", user.Email).
		Where("id = ?", user.ID)

	_, err := r.squirrelHelper.Exec(ctx, r.GetQuerier(), updateBuilder)
	return err
}

// DeleteUser deletes a user using Squirrel
func (r *UserRepository) DeleteUser(ctx context.Context, id int) error {
	deleteBuilder := r.Builder().
		Delete("users").
		Where("id = ?", id)

	_, err := r.squirrelHelper.Exec(ctx, r.GetQuerier(), deleteBuilder)
	return err
}

// FindUsersByNamePrefix finds users by name prefix using Squirrel
func (r *UserRepository) FindUsersByNamePrefix(ctx context.Context, prefix string) ([]User, error) {
	var users []User

	selectBuilder := r.Builder().
		Select("id", "name", "email", "created_at").
		From("users").
		Where("name LIKE ?", prefix+"%").
		OrderBy("name ASC")

	rows, err := r.squirrelHelper.Query(ctx, r.GetQuerier(), selectBuilder)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var user User
		err := rows.Scan(&user.ID, &user.Name, &user.Email, &user.CreatedAt)
		if err != nil {
			return nil, err
		}
		users = append(users, user)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return users, nil
}

// UserService handles operations with users
type UserService struct {
	transactor *pgxtransactor.Transactor
	userRepo   *UserRepository
}

// NewUserService creates a new UserService
func NewUserService(
	transactor *pgxtransactor.Transactor,
	userRepo *UserRepository,
) *UserService {
	return &UserService{
		transactor: transactor,
		userRepo:   userRepo,
	}
}

// CreateAndUpdateUser creates a user then immediately updates them in a transaction
func (s *UserService) CreateAndUpdateUser(ctx context.Context, user User, newEmail string) error {
	// Define repositories that will participate in the transaction
	storages := []pgxtransactor.Storage{s.userRepo}

	// Execute operations in a transaction
	return s.transactor.ExecuteInTx(ctx, storages, func() error {
		// Create user
		if err := s.userRepo.CreateUser(ctx, user); err != nil {
			return fmt.Errorf("failed to create user: %w", err)
		}

		// Update user's email
		user.Email = newEmail
		if err := s.userRepo.UpdateUser(ctx, user); err != nil {
			return fmt.Errorf("failed to update user: %w", err)
		}

		return nil
	})
}

func main() {
	// Set up the connection to the database
	ctx := context.Background()
	connString := "postgres://user:password@localhost:5432/dbname"
	pool, err := pgxpool.New(ctx, connString)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer pool.Close()

	// Create repository
	userRepo := NewUserRepository(pool)

	// Create transactor
	transactor := pgxtransactor.NewTransactor(pool)

	// Create service
	userService := NewUserService(transactor, userRepo)

	// Use the service
	user := User{Name: "John Doe", Email: "john@example.com"}
	newEmail := "john.doe@company.com"

	err = userService.CreateAndUpdateUser(ctx, user, newEmail)
	if err != nil {
		log.Fatalf("Failed to create and update user: %v", err)
	}

	fmt.Println("User created and updated successfully!")

	// Example of using repository method directly (outside transaction)
	users, err := userRepo.FindUsersByNamePrefix(ctx, "Jo")
	if err != nil {
		log.Fatalf("Failed to find users: %v", err)
	}

	for _, u := range users {
		fmt.Printf("Found user: %s (%s)\n", u.Name, u.Email)
	}
}
