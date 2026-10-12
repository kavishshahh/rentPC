-- Rentals paid up-front through an external payment protocol (MPP on Tempo,
-- later x402 on Solana). Settlement at the end of the rental is skipped for
-- these; the ledger rows are written at reservation time.
ALTER TABLE rentals ADD COLUMN prepaid_provider text;
ALTER TABLE rentals ADD COLUMN prepaid_ref text;
