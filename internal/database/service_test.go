package database

import (
	"context"
	"testing"
	"time"
)

func TestOperationGateAndCancellation(t *testing.T) {
	s := NewService(nil)
	ctx, _, finish, err := s.begin(false, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.begin(false, time.Minute); err == nil {
		t.Fatal("allowed a second active operation")
	}
	if err := s.CancelQuery(); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != context.Canceled {
		t.Fatalf("operation not canceled: %v", ctx.Err())
	}
	finish()
	_, _, finish, err = s.begin(false, time.Minute)
	if err != nil {
		t.Fatalf("gate not released: %v", err)
	}
	finish()
	s.Shutdown()
	if _, _, _, err := s.begin(false, time.Minute); err == nil {
		t.Fatal("allowed an operation after shutdown")
	}
}
