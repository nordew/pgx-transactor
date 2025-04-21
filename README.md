# pgx-transactor

A lightweight, idiomatic Go library that makes PostgreSQL transaction management simple and reliable with the [pgx](https://github.com/jackc/pgx) driver.

[![Go Reference](https://pkg.go.dev/badge/github.com/nordew/pgx-transactor.svg)](https://pkg.go.dev/github.com/nordew/pgx-transactor)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

## 🚀 Features

- ✅ **Easy transaction handling** - Automatic commit on success, rollback on error
- 🔄 **Simple retries** - Built-in retry mechanism for serializable transactions
- 🛡️ **Better error handling** - Helpers to identify PostgreSQL constraint violations
- ⚙️ **Flexible configuration** - Control isolation levels with convenient presets
- 📝 **Context support** - Full integration with Go's context package
- 🔗 **Service layer integration** - Handle transactions at the service level, not in repositories
- 📊 **Squirrel support** - Optional integration with the Squirrel SQL query builder
- 🪶 **Lightweight** - Minimal dependencies (only pgx and optionally Squirrel)

## 📦 Installation

```bash
go get github.com/nordew/pgx-transactor
```

## 🏁 Quick Start

### Service-layer transaction management

```go
// Set up repositories
userRepo := NewUserRepository(pool)
orderRepo := NewOrderRepository(pool)

// Create a transactor
transactor := pgxtransactor.NewTransactor(pool)

// Execute multiple repository operations in a single transaction
err := transactor.ExecuteInTx(ctx, []pgxtransactor.Storage{userRepo, orderRepo}, func() error {
    // Create user
    if err := userRepo.CreateUser(ctx, user); err != nil {
        return err
    }

    // Create order
    if err := orderRepo.CreateOrder(ctx, order); err != nil {
        return err
    }

    return nil
})
```

### Traditional transaction management (still supported)

```go
// Connect to the database
pool, err := pgxpool.New(ctx, "postgres://user:password@localhost:5432/mydb")
if err != nil {
    log.Fatalf("Unable to connect to database: %v\n", err)
}
defer pool.Close()

// Create a transactor
transactor := pgxtransactor.NewTransactor(pool)

// Use a transaction - commits automatically if successful, rolls back on error
err = transactor.WithTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
    // Execute queries within the transaction
    _, err := tx.Exec(ctx, "INSERT INTO users(name) VALUES($1)", "John Doe")
    if err != nil {
        return err // Will cause rollback
    }

    // Add more operations that should be part of the same transaction
    _, err = tx.Exec(ctx, "UPDATE accounts SET balance = balance - $1 WHERE user_id = $2", 100, 1)
    return err // Transaction commits if nil, rolls back otherwise
})
```

## 🧩 Examples

### Creating storage components

```go
// Define a repository that implements the Storage interface
type UserRepository struct {
    pgxtransactor.Storage
}

// Create a new repository with the connection pool
func NewUserRepository(pool *pgxpool.Pool) *UserRepository {
    return &UserRepository{
        Storage: pgxtransactor.NewBaseStorage(pool),
    }
}

// Repository methods use GetQuerier() for database operations
func (r *UserRepository) CreateUser(ctx context.Context, user User) error {
    _, err := r.GetQuerier().Exec(
        ctx,
        "INSERT INTO users (name, email) VALUES ($1, $2)",
        user.Name, user.Email,
    )
    return err
}
```

### Service layer with multiple repositories

```go
type UserService struct {
    transactor      *pgxtransactor.Transactor
    userRepository  *UserRepository
    orderRepository *OrderRepository
}

func (s *UserService) CreateUserWithOrder(ctx context.Context, user User, order Order) error {
    // Define all repositories that will participate in the transaction
    storages := []pgxtransactor.Storage{
        s.userRepository,
        s.orderRepository,
    }

    // Execute operations in a transaction
    return s.transactor.ExecuteInTx(ctx, storages, func() error {
        // Create user
        if err := s.userRepository.CreateUser(ctx, user); err != nil {
            return err
        }

        // Create order
        if err := s.orderRepository.CreateOrder(ctx, order); err != nil {
            return err
        }

        return nil
    })
}
```

### Using with Squirrel SQL builder

```go
// Define a repository that uses Squirrel
type ProductRepository struct {
    pgxtransactor.Storage
    squirrelHelper *pgxtransactor.SquirrelHelper
}

// Create a new repository with the connection pool
func NewProductRepository(pool *pgxpool.Pool) *ProductRepository {
    return &ProductRepository{
        Storage:        pgxtransactor.NewBaseStorage(pool),
        squirrelHelper: pgxtransactor.NewSquirrelHelper(),
    }
}

// Use Squirrel to build and execute queries
func (r *ProductRepository) FindProductsByCategory(ctx context.Context, category string) ([]Product, error) {
    // Build query with Squirrel
    selectBuilder := r.Builder().
        Select("id", "name", "price", "category").
        From("products").
        Where("category = ?", category).
        OrderBy("name ASC")

    // Execute the query
    rows, err := r.squirrelHelper.Query(ctx, r.GetQuerier(), selectBuilder)
    if err != nil {
        return nil, err
    }
    defer rows.Close()

    // Process results
    var products []Product
    for rows.Next() {
        var p Product
        if err := rows.Scan(&p.ID, &p.Name, &p.Price, &p.Category); err != nil {
            return nil, err
        }
        products = append(products, p)
    }

    return products, rows.Err()
}
```

### Read-only transactions

```go
err = transactor.ExecuteInReadOnlyTx(ctx, []pgxtransactor.Storage{userRepo}, func() error {
    users, err := userRepo.GetAllUsers(ctx)
    if err != nil {
        return err
    }

    // Process users (read-only operations)
    return nil
})
```

### Automatic retries for serializable transactions

```go
// Try up to 3 times to handle serialization conflicts
err = transactor.ExecuteWithRetry(ctx, 3, []pgxtransactor.Storage{accountRepo}, func() error {
    return accountRepo.TransferFunds(ctx, fromID, toID, amount)
})
```

### Better error handling

```go
if err := userService.CreateUser(ctx, user); err != nil {
    switch {
    case pgxtransactor.IsUniqueViolation(err):
        return "This email is already registered"
    case pgxtransactor.IsForeignKeyViolation(err):
        return "Referenced account doesn't exist"
    default:
        return "Something went wrong, please try again"
    }
}
```

## 📚 Usage Patterns

### Repository pattern with BaseStorage

```go
type UserRepository struct {
    pgxtransactor.Storage
}

func NewUserRepository(pool *pgxpool.Pool) *UserRepository {
    return &UserRepository{
        Storage: pgxtransactor.NewBaseStorage(pool),
    }
}

func (r *UserRepository) CreateUser(ctx context.Context, user User) error {
    _, err := r.GetQuerier().Exec(ctx,
        "INSERT INTO users(name, email) VALUES($1, $2)",
        user.Name, user.Email)
    return err
}
```

### Service layer with transaction management

```go
type OrderService struct {
    transactor  *pgxtransactor.Transactor
    orderRepo   *OrderRepository
    productRepo *ProductRepository
    userRepo    *UserRepository
}

func (s *OrderService) PlaceOrder(ctx context.Context, order Order) error {
    storages := []pgxtransactor.Storage{s.orderRepo, s.productRepo, s.userRepo}

    return s.transactor.ExecuteInTx(ctx, storages, func() error {
        // All these operations succeed or fail together
        if err := s.productRepo.CheckInventory(ctx, order.Items); err != nil {
            return err
        }

        if err := s.orderRepo.CreateOrder(ctx, order); err != nil {
            return err
        }

        if err := s.productRepo.UpdateInventory(ctx, order.Items); err != nil {
            return err
        }

        if err := s.userRepo.UpdateOrderCount(ctx, order.UserID); err != nil {
            return err
        }

        return nil
    })
}
```

## 📋 API Overview

### Service Layer Transaction Management
- `ExecuteInTx` - Execute operations from multiple repositories in a single transaction
- `ExecuteInReadOnlyTx` - Execute read-only operations
- `ExecuteInSerializableTx` - Execute operations with serializable isolation
- `ExecuteInTxWithOptions` - Execute operations with custom transaction options
- `ExecuteWithRetry` - Execute operations with automatic retry for serialization failures

### Traditional Transaction Management (still supported)
- `WithTx` - Standard transaction with sensible defaults
- `WithReadOnlyTx` - Read-only transaction for queries
- `WithSerializableTx` - Transaction with serializable isolation
- `WithTxOptions` - Custom transaction options
- `WithRetry` - Automatic retry for serializable transactions

### Error Helpers
- `IsUniqueViolation`, `IsForeignKeyViolation`, etc. - Error type helpers

### Squirrel Integration
- `SquirrelHelper` - Helpers to execute Squirrel query builders with pgx
- `Builder()` - Get a Squirrel StatementBuilder from any Storage component

## 📄 License

R
