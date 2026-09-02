# Library Inventory API

A library management REST API in Go: members borrow and return books, stock is
tracked per title, and access is scoped by JWT role. Built with Gin and MongoDB.

The interesting problem here is **stock integrity under concurrency** — making it
impossible to lend the same physical copy to two people at once. See
[Preventing overselling](#preventing-overselling).

---

## Stack

| | |
|---|---|
| Language | Go 1.24 |
| HTTP | Gin |
| Database | MongoDB (official driver v2) |
| Auth | JWT (HS256) + bcrypt |
| Logging | `log/slog`, JSON in production |
| Container | Multi-stage Docker → distroless, non-root |

## Quick start

```bash
docker compose up --build
```

That starts MongoDB and the API together, waiting for the database healthcheck
before the API boots. The service listens on `http://localhost:8080`.

Without Docker, point the service at any MongoDB instance:

```bash
export MONGO_URI="mongodb://localhost:27017"
make run
```

**The first account to register becomes a librarian.** A fresh deployment
otherwise has nobody who can add books, and this avoids a separate seeding step.
Every account after that is a member.

```bash
# 1. Register — this first one gets the librarian role
curl -s -X POST localhost:8080/api/v1/auth/register \
  -H 'Content-Type: application/json' \
  -d '{"name":"Palash","email":"me@example.com","password":"a-good-password"}'

# 2. Keep the token
TOKEN=$(curl -s -X POST localhost:8080/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"me@example.com","password":"a-good-password"}' \
  | jq -r .data.token)

# 3. Add a book
curl -s -X POST localhost:8080/api/v1/books \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"title":"Designing Data-Intensive Applications","author":"Martin Kleppmann",
       "isbn":"9781449373320","category":"engineering","published_year":2017,
       "total_copies":3}'

# 4. Borrow it
curl -s -X POST localhost:8080/api/v1/loans/borrow \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "{\"book_id\":\"<id from step 3>\"}"
```

## Endpoints

Every route below `/api/v1` except register and login requires
`Authorization: Bearer <token>`.

| Method | Path | Scope | Purpose |
|---|---|---|---|
| `GET` | `/health` | public | Liveness + database reachability |
| `POST` | `/api/v1/auth/register` | public | Create an account |
| `POST` | `/api/v1/auth/login` | public | Exchange credentials for a token |
| `GET` | `/api/v1/auth/me` | any | The caller's own account |
| `GET` | `/api/v1/books` | any | Search catalogue, paginated |
| `GET` | `/api/v1/books/:id` | any | One book |
| `POST` | `/api/v1/books` | librarian | Add to catalogue |
| `PUT` | `/api/v1/books/:id` | librarian | Partial update, incl. stock |
| `DELETE` | `/api/v1/books/:id` | librarian | Remove (blocked while on loan) |
| `POST` | `/api/v1/loans/borrow` | any | Borrow a copy |
| `POST` | `/api/v1/loans/:id/return` | owner or librarian | Return a copy |
| `GET` | `/api/v1/loans/me` | any | The caller's borrowing history |
| `GET` | `/api/v1/loans` | librarian | All loans, filterable |
| `PATCH` | `/api/v1/users/:id/role` | librarian | Promote or demote |

Catalogue listing accepts `?search=`, `?category=`, `?available=true`, `?page=`
and `?per_page=`. Loan listings accept `?status=active|returned` and
`?overdue=true`.

### Response shape

Every response uses one envelope, so a client never has to guess:

```jsonc
// success
{ "status": "success", "data": { ... } }

// list, with pagination
{ "status": "success", "data": [ ... ],
  "meta": { "page": 1, "per_page": 20, "total": 42, "total_pages": 3 } }

// failure
{ "status": "error", "code": "NOT_FOUND", "message": "book not found" }

// validation failure — every offending field, not just the first
{ "status": "error", "code": "VALIDATION_ERROR",
  "message": "request validation failed",
  "errors": [ { "field": "isbn", "message": "must be a valid ISBN-10 or ISBN-13" } ] }
```

Codes are `VALIDATION_ERROR`, `UNAUTHORIZED`, `FORBIDDEN`, `NOT_FOUND`,
`CONFLICT`, `INTERNAL_ERROR`. Clients branch on these rather than on message text.

---

## Design notes

### Preventing overselling

A book with one copy left must go to exactly one of two simultaneous borrowers.
The obvious implementation is wrong:

```go
book := findBook(id)            // both goroutines read available == 1
if book.AvailableCopies > 0 {   // both pass the check
    book.AvailableCopies--      // both write 0; two loans, one copy
    save(book)
}
```

The check and the write are separate operations, so another request can land
between them. The fix is to make the precondition part of the write itself, and
let MongoDB's single-document atomicity settle the race:

```go
filter := bson.M{"_id": id, "available_copies": bson.M{"$gt": 0}}
update := bson.M{"$inc": bson.M{"available_copies": -1}}
err := col.FindOneAndUpdate(ctx, filter, update, returnAfter).Decode(&book)
```

Whichever request the server orders second no longer matches the filter, and gets
`ErrNoCopies` instead of a copy that isn't there. No transaction and no
application-level lock — which matters, because multi-document transactions need
a replica set, and this runs against a standalone `mongod`.

`TestConcurrentBorrowsNeverOversell` races 30 borrowers for 3 copies and asserts
exactly 3 succeed. Reverting the repository to the read-then-write version above
makes it fail with 16 successful loans and stock at −13.

The same reasoning guards the way back: `MarkReturned` matches on
`status == "active"`, so a replayed return closes nothing and cannot increment
stock a second time.

### Borrow is a two-step write, so it compensates

Borrowing reserves the copy and then writes the loan. Without multi-document
transactions, a failure between those steps would strand the copy as permanently
unavailable — so the handler releases the reservation explicitly when the loan
write fails. `TestBorrowReleasesStockWhenLoanCreationFails` covers it.

### Validation happens in middleware, not handlers

`ValidateJSON[T]` is a generic middleware that binds and validates the body,
returning 422 with every offending field before the handler runs:

```go
b.POST("", librarian, middleware.ValidateJSON[models.CreateBookRequest](), bookH.Create)
```

The handler then reads `middleware.Payload[models.CreateBookRequest](c)` and can
assume it is valid. No handler repeats bind-and-check boilerplate, and none can
forget the error branch. Field names in errors are the JSON ones the client sent
(`published_year`, not `PublishedYear`) via a tag-name function on the validator.

### JWT scoping

Tokens carry the user id and role, so authorising a request costs no database
round trip. Roles are checked against an explicit allow-list per route
(`RequireRole(models.RoleLibrarian)`) rather than by ranking roles and comparing
— ordering by "power" is how privilege bugs get written.

The parser pins the signing method to HMAC. Without that check, a token with
`alg: none` carries no signature to verify and a naive parser accepts whatever
claims the caller wrote. `TestParseRejectsAlgNone` covers it.

Login runs a bcrypt comparison against a dummy hash when no account matches, so
response timing doesn't reveal which email addresses are registered.

### Structured logging

`log/slog` throughout — JSON in production, text locally. Every request gets a
correlation id (reused from an inbound `X-Request-ID` when present, so a trace
survives across services) and a request-scoped logger, so anything a handler logs
is automatically tied to the request that caused it. Log level follows the
response class: 5xx is `error`, 4xx is `warn`, everything else `info`.

```json
{"time":"2026-09-02T14:22:31Z","level":"INFO","msg":"book borrowed",
 "service":"library-inventory-api","request_id":"9f2c1a4e8b7d3f60",
 "method":"POST","path":"/api/v1/loans/borrow","user_id":"66d5...","copies_left":2}
```

### Indexes

Created idempotently at startup. Two are load-bearing rather than merely fast:
unique on `users.email` and on `books.isbn`, which is what makes concurrent
registration of the same address fail for one caller instead of silently
producing a duplicate account.

---

## Layout

```
cmd/api/            entrypoint, graceful shutdown
internal/
  config/           environment loading, refuses the dev secret in production
  models/           domain types and request DTOs
  repository/       storage interfaces + MongoDB implementations
  handlers/         HTTP endpoints and routing
  middleware/       auth, validation, logging, request id, recovery
  auth/             JWT issue/verify, password hashing
  db/               connection and index management
  httpx/            response envelope and error codes
```

Handlers depend on repository *interfaces*, not on Mongo types. That is what lets
the whole HTTP layer — routing, middleware, auth, borrow/return logic — be tested
against in-memory fakes with no database running.

## Testing

```bash
make test        # 31 tests
make test-race   # same, under the race detector
make cover       # HTML coverage report
```

Coverage is deliberately concentrated on the parts where a bug is expensive:
stock integrity under concurrency, double-return, quota enforcement, role
boundaries, and token verification.

| Package | Coverage |
|---|---|
| `config` | 95.7% |
| `auth` | 82.1% |
| `handlers` | 42.6% (drives `middleware` and `httpx` end to end) |

The MongoDB repository implementations are not unit tested — verifying them
against a real database is what integration tests are for, and asserting that a
mocked driver was called with a particular BSON document tests the mock rather
than the query. The fakes mirror their atomicity contract instead, and the
mutation described above is the check that they mirror it faithfully.

## Configuration

All via environment variables; every one has a working default except in
production. See [`.env.example`](.env.example).

| Variable | Default | Notes |
|---|---|---|
| `APP_ENV` | `development` | `production` → JSON logs, Gin release mode |
| `PORT` | `8080` | |
| `MONGO_URI` | `mongodb://localhost:27017` | |
| `MONGO_DATABASE` | `library` | |
| `JWT_SECRET` | dev placeholder | **Startup fails if left at the default when `APP_ENV=production`** |
| `JWT_TTL` | `24h` | |
| `LOAN_PERIOD` | `336h` (14 days) | |
| `MAX_ACTIVE_LOANS` | `5` | |
| `LOG_LEVEL` | `info` | |

## Known limitations

Honest about what this does and doesn't do:

- **No refresh tokens.** A role change only takes effect once the holder's
  current token expires. Fine at this TTL; a real deployment would want
  short-lived access tokens plus refresh.
- **Catalogue search is an unanchored regex**, which cannot use an index. Correct
  and fast enough at library scale; Atlas Search would be the move beyond that.
- **Borrow compensates rather than transacts.** A replica set would allow a real
  multi-document transaction and remove the compensation path.
- **No rate limiting** on the login endpoint.
