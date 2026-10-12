-- Hosts can be paid on-chain directly: the MPP challenge splits the renter's
-- single transfer between the host's wallet and the platform treasury.
ALTER TABLE users ADD COLUMN payout_address text;
ALTER TABLE rentals ADD COLUMN host_payout_cents bigint;
ALTER TABLE rentals ADD COLUMN host_payout_address text;
