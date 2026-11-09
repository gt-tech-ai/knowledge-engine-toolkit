package interfaces

// InsertOnlyStore is the append-only, tx-aware persistence seam: a store whose Insert
// honors a transaction on the context.
type InsertOnlyStore[T any] interface {
	// Inserter contributes Insert (tx-aware append-only write).
	Inserter[T]
}
