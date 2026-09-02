package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/v2/bson"

	"go-server/internal/auth"
	"go-server/internal/config"
	"go-server/internal/middleware"
	"go-server/internal/models"
)

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	// The custom "mongoid" rule is registered on gin's process-global validator,
	// so it has to be installed before any request is routed.
	if err := middleware.RegisterValidators(); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

// harness is a fully wired API backed by in-memory repositories.
type harness struct {
	router *gin.Engine
	users  *fakeUserRepo
	books  *fakeBookRepo
	loans  *fakeLoanRepo
	tokens *auth.TokenManager
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	cfg := &config.Config{
		Env:            "test",
		JWTSecret:      "a-sufficiently-long-test-secret",
		JWTTTL:         time.Hour,
		LoanPeriod:     14 * 24 * time.Hour,
		MaxActiveLoans: 3,
	}

	h := &harness{
		users:  newFakeUserRepo(),
		books:  newFakeBookRepo(),
		loans:  newFakeLoanRepo(),
		tokens: auth.NewTokenManager(cfg.JWTSecret, cfg.JWTTTL),
	}

	h.router = NewRouter(Deps{
		Config: cfg,
		// Discard log output so a passing test run stays readable.
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Tokens: h.tokens,
		Users:  h.users,
		Books:  h.books,
		Loans:  h.loans,
	})
	return h
}

// account creates a user with the given role and returns a valid token for them.
func (h *harness) account(t *testing.T, role models.Role) (string, bson.ObjectID) {
	t.Helper()

	user := &models.User{
		Name:  "Test Reader",
		Email: fmt.Sprintf("reader-%s@example.com", bson.NewObjectID().Hex()),
		Role:  role,
	}
	if err := h.users.Create(t.Context(), user); err != nil {
		t.Fatalf("seeding user: %v", err)
	}

	token, _, err := h.tokens.Generate(user)
	if err != nil {
		t.Fatalf("generating token: %v", err)
	}
	return token, user.ID
}

// do issues a request against the router, attaching token as a bearer credential
// when non-empty.
func (h *harness) do(t *testing.T, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshalling body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}

	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec
}

// decode pulls the "data" object out of a success envelope.
func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()

	var envelope struct {
		Status string         `json:"status"`
		Data   map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decoding response %q: %v", rec.Body.String(), err)
	}
	return envelope.Data
}

func TestBorrowDecrementsAvailableStock(t *testing.T) {
	h := newHarness(t)
	token, _ := h.account(t, models.RoleMember)
	bookID := h.books.seed("The Go Programming Language", 2)

	rec := h.do(t, http.MethodPost, "/api/v1/loans/borrow", token,
		models.BorrowRequest{BookID: bookID.Hex()})

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", rec.Code, rec.Body)
	}
	if got := h.books.available(bookID); got != 1 {
		t.Errorf("available copies = %d, want 1", got)
	}

	data := decode(t, rec)
	if data["status"] != string(models.LoanActive) {
		t.Errorf("loan status = %v, want %q", data["status"], models.LoanActive)
	}
	if data["overdue"] != false {
		t.Errorf("overdue = %v, want false for a new loan", data["overdue"])
	}
}

func TestBorrowLastCopyThenNextBorrowerIsRefused(t *testing.T) {
	h := newHarness(t)
	first, _ := h.account(t, models.RoleMember)
	second, _ := h.account(t, models.RoleMember)
	bookID := h.books.seed("Sole Copy", 1)

	if rec := h.do(t, http.MethodPost, "/api/v1/loans/borrow", first,
		models.BorrowRequest{BookID: bookID.Hex()}); rec.Code != http.StatusCreated {
		t.Fatalf("first borrow status = %d, want 201", rec.Code)
	}

	rec := h.do(t, http.MethodPost, "/api/v1/loans/borrow", second,
		models.BorrowRequest{BookID: bookID.Hex()})
	if rec.Code != http.StatusConflict {
		t.Fatalf("second borrow status = %d, want 409; body = %s", rec.Code, rec.Body)
	}
	if got := h.books.available(bookID); got != 0 {
		t.Errorf("available copies = %d, want 0", got)
	}
}

// TestConcurrentBorrowsNeverOversell is the load-bearing test for stock
// integrity: many readers race for a small number of copies, and the number of
// successful loans must exactly equal the stock that existed. A read-then-write
// reservation fails this and drives available copies negative.
func TestConcurrentBorrowsNeverOversell(t *testing.T) {
	const (
		copies    = 3
		borrowers = 30
	)

	h := newHarness(t)
	bookID := h.books.seed("Contended Title", copies)

	tokens := make([]string, borrowers)
	for i := range tokens {
		tokens[i], _ = h.account(t, models.RoleMember)
	}

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		created  int
		conflict int
		other    []int
	)

	start := make(chan struct{})
	for i := range borrowers {
		wg.Add(1)
		go func(token string) {
			defer wg.Done()
			<-start // release every goroutine at once to maximise contention

			rec := h.do(t, http.MethodPost, "/api/v1/loans/borrow", token,
				models.BorrowRequest{BookID: bookID.Hex()})

			mu.Lock()
			defer mu.Unlock()
			switch rec.Code {
			case http.StatusCreated:
				created++
			case http.StatusConflict:
				conflict++
			default:
				other = append(other, rec.Code)
			}
		}(tokens[i])
	}
	close(start)
	wg.Wait()

	if len(other) > 0 {
		t.Errorf("unexpected status codes = %v, want only 201 and 409", other)
	}
	if created != copies {
		t.Errorf("successful borrows = %d, want exactly %d", created, copies)
	}
	if conflict != borrowers-copies {
		t.Errorf("refused borrows = %d, want %d", conflict, borrowers-copies)
	}
	if got := h.books.available(bookID); got != 0 {
		t.Errorf("available copies = %d, want 0 (negative means overselling)", got)
	}
}

func TestBorrowingSameBookTwiceIsRefused(t *testing.T) {
	h := newHarness(t)
	token, _ := h.account(t, models.RoleMember)
	bookID := h.books.seed("Duplicate Target", 5)

	body := models.BorrowRequest{BookID: bookID.Hex()}
	if rec := h.do(t, http.MethodPost, "/api/v1/loans/borrow", token, body); rec.Code != http.StatusCreated {
		t.Fatalf("first borrow status = %d, want 201", rec.Code)
	}

	rec := h.do(t, http.MethodPost, "/api/v1/loans/borrow", token, body)
	if rec.Code != http.StatusConflict {
		t.Errorf("second borrow status = %d, want 409", rec.Code)
	}
	if got := h.books.available(bookID); got != 4 {
		t.Errorf("available copies = %d, want 4 (refused borrow must not consume stock)", got)
	}
}

func TestBorrowingBeyondQuotaIsRefused(t *testing.T) {
	h := newHarness(t)
	token, _ := h.account(t, models.RoleMember)

	// MaxActiveLoans is 3 in the harness, so the fourth distinct title must fail.
	for i := range 3 {
		id := h.books.seed(fmt.Sprintf("Title %d", i), 1)
		if rec := h.do(t, http.MethodPost, "/api/v1/loans/borrow", token,
			models.BorrowRequest{BookID: id.Hex()}); rec.Code != http.StatusCreated {
			t.Fatalf("borrow %d status = %d, want 201", i, rec.Code)
		}
	}

	overQuota := h.books.seed("One Too Many", 1)
	rec := h.do(t, http.MethodPost, "/api/v1/loans/borrow", token,
		models.BorrowRequest{BookID: overQuota.Hex()})

	if rec.Code != http.StatusConflict {
		t.Errorf("over-quota borrow status = %d, want 409", rec.Code)
	}
	if got := h.books.available(overQuota); got != 1 {
		t.Errorf("available copies = %d, want 1 (quota rejection must not consume stock)", got)
	}
}

// TestBorrowReleasesStockWhenLoanCreationFails covers the compensation path: the
// copy is reserved before the loan row is written, so a failure in between must
// put it back or the copy is stranded as permanently unavailable.
func TestBorrowReleasesStockWhenLoanCreationFails(t *testing.T) {
	h := newHarness(t)
	token, _ := h.account(t, models.RoleMember)
	bookID := h.books.seed("Doomed Loan", 1)

	h.loans.createErr = fmt.Errorf("simulated write failure")

	rec := h.do(t, http.MethodPost, "/api/v1/loans/borrow", token,
		models.BorrowRequest{BookID: bookID.Hex()})

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body = %s", rec.Code, rec.Body)
	}
	if got := h.books.available(bookID); got != 1 {
		t.Errorf("available copies = %d, want 1; the reserved copy was not released", got)
	}
}

func TestReturnRestoresStock(t *testing.T) {
	h := newHarness(t)
	token, _ := h.account(t, models.RoleMember)
	bookID := h.books.seed("Returnable", 1)

	borrowRec := h.do(t, http.MethodPost, "/api/v1/loans/borrow", token,
		models.BorrowRequest{BookID: bookID.Hex()})
	loanID, _ := decode(t, borrowRec)["id"].(string)

	rec := h.do(t, http.MethodPost, "/api/v1/loans/"+loanID+"/return", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body)
	}
	if got := h.books.available(bookID); got != 1 {
		t.Errorf("available copies = %d, want 1", got)
	}
	if data := decode(t, rec); data["status"] != string(models.LoanReturned) {
		t.Errorf("loan status = %v, want %q", data["status"], models.LoanReturned)
	}
}

// TestDoubleReturnDoesNotInflateStock checks the status-guarded close: without
// it, replaying a return would increment available copies past what the library
// owns and invent a book out of nothing.
func TestDoubleReturnDoesNotInflateStock(t *testing.T) {
	h := newHarness(t)
	token, _ := h.account(t, models.RoleMember)
	bookID := h.books.seed("Twice Returned", 1)

	borrowRec := h.do(t, http.MethodPost, "/api/v1/loans/borrow", token,
		models.BorrowRequest{BookID: bookID.Hex()})
	loanID, _ := decode(t, borrowRec)["id"].(string)

	path := "/api/v1/loans/" + loanID + "/return"
	if rec := h.do(t, http.MethodPost, path, token, nil); rec.Code != http.StatusOK {
		t.Fatalf("first return status = %d, want 200", rec.Code)
	}

	rec := h.do(t, http.MethodPost, path, token, nil)
	if rec.Code != http.StatusConflict {
		t.Errorf("second return status = %d, want 409", rec.Code)
	}
	if got := h.books.available(bookID); got != 1 {
		t.Errorf("available copies = %d, want 1 (stock was inflated by the replay)", got)
	}
}

func TestMemberCannotReturnAnotherMembersLoan(t *testing.T) {
	h := newHarness(t)
	owner, _ := h.account(t, models.RoleMember)
	stranger, _ := h.account(t, models.RoleMember)
	bookID := h.books.seed("Someone Elses", 1)

	borrowRec := h.do(t, http.MethodPost, "/api/v1/loans/borrow", owner,
		models.BorrowRequest{BookID: bookID.Hex()})
	loanID, _ := decode(t, borrowRec)["id"].(string)

	rec := h.do(t, http.MethodPost, "/api/v1/loans/"+loanID+"/return", stranger, nil)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
	if got := h.books.available(bookID); got != 0 {
		t.Errorf("available copies = %d, want 0 (forbidden return must not restore stock)", got)
	}
}

// A librarian takes books back over the desk, so they may close anyone's loan.
func TestLibrarianCanReturnAnotherMembersLoan(t *testing.T) {
	h := newHarness(t)
	owner, _ := h.account(t, models.RoleMember)
	librarian, _ := h.account(t, models.RoleLibrarian)
	bookID := h.books.seed("Over The Desk", 1)

	borrowRec := h.do(t, http.MethodPost, "/api/v1/loans/borrow", owner,
		models.BorrowRequest{BookID: bookID.Hex()})
	loanID, _ := decode(t, borrowRec)["id"].(string)

	rec := h.do(t, http.MethodPost, "/api/v1/loans/"+loanID+"/return", librarian, nil)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body = %s", rec.Code, rec.Body)
	}
}

func TestMemberCannotCreateBooks(t *testing.T) {
	h := newHarness(t)
	token, _ := h.account(t, models.RoleMember)

	rec := h.do(t, http.MethodPost, "/api/v1/books", token, models.CreateBookRequest{
		Title:         "Unauthorised Addition",
		Author:        "A Member",
		ISBN:          "9780306406157",
		Category:      "fiction",
		PublishedYear: 2020,
		TotalCopies:   1,
	})

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403; body = %s", rec.Code, rec.Body)
	}
}

func TestLibrarianCanCreateBooks(t *testing.T) {
	h := newHarness(t)
	token, _ := h.account(t, models.RoleLibrarian)

	rec := h.do(t, http.MethodPost, "/api/v1/books", token, models.CreateBookRequest{
		Title:         "Designing Data-Intensive Applications",
		Author:        "Martin Kleppmann",
		ISBN:          "9781449373320",
		Category:      "engineering",
		PublishedYear: 2017,
		TotalCopies:   4,
	})

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", rec.Code, rec.Body)
	}

	data := decode(t, rec)
	// A new book must start fully on the shelf.
	if data["available_copies"] != float64(4) {
		t.Errorf("available_copies = %v, want 4", data["available_copies"])
	}
}

func TestValidationErrorsNameEveryOffendingField(t *testing.T) {
	h := newHarness(t)
	token, _ := h.account(t, models.RoleLibrarian)

	// Missing title and author, an invalid ISBN and an out-of-range year.
	rec := h.do(t, http.MethodPost, "/api/v1/books", token, map[string]any{
		"isbn":           "not-an-isbn",
		"category":       "fiction",
		"published_year": 900,
		"total_copies":   1,
	})

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", rec.Code, rec.Body)
	}

	var envelope struct {
		Code   string `json:"code"`
		Errors []struct {
			Field   string `json:"field"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	if envelope.Code != "VALIDATION_ERROR" {
		t.Errorf("code = %q, want VALIDATION_ERROR", envelope.Code)
	}

	got := map[string]bool{}
	for _, fe := range envelope.Errors {
		got[fe.Field] = true
	}
	// Field names must be the JSON ones the client sent, not Go struct names.
	for _, want := range []string{"title", "author", "isbn", "published_year"} {
		if !got[want] {
			t.Errorf("missing field error for %q; got %v", want, got)
		}
	}
}

func TestBorrowRejectsMalformedBookID(t *testing.T) {
	h := newHarness(t)
	token, _ := h.account(t, models.RoleMember)

	rec := h.do(t, http.MethodPost, "/api/v1/loans/borrow", token,
		map[string]any{"book_id": "obviously-not-an-object-id"})

	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422; body = %s", rec.Code, rec.Body)
	}
}

func TestProtectedEndpointsRejectMissingAndBadTokens(t *testing.T) {
	h := newHarness(t)

	cases := []struct {
		name  string
		token string
	}{
		{"no token", ""},
		{"garbage token", "not-a-real-token"},
		{"token signed by someone else", func() string {
			other := auth.NewTokenManager("a-different-signing-secret-entirely", time.Hour)
			token, _, _ := other.Generate(&models.User{ID: bson.NewObjectID(), Role: models.RoleLibrarian})
			return token
		}()},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := h.do(t, http.MethodGet, "/api/v1/books", tc.token, nil)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401; body = %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestEveryResponseCarriesARequestID(t *testing.T) {
	h := newHarness(t)
	token, _ := h.account(t, models.RoleMember)

	rec := h.do(t, http.MethodGet, "/api/v1/books", token, nil)
	if rec.Header().Get(middleware.RequestIDHeader) == "" {
		t.Errorf("missing %s header", middleware.RequestIDHeader)
	}
}
