package main

// BEP-20 withdrawal and signer integration will be implemented after
// ledger, risk checks, admin approval, and isolated signing are ready.
//
// Hybrid policy selected:
// - automatic payout below an Admin-configurable threshold, when eligible;
// - manual Admin review above the threshold or whenever risk checks require it.
//
// Never store a Bitget wallet private key or seed phrase in this application.
