-- The OS the owner says the machine runs; drives setup instructions before
-- the agent has reported anything. Confirmed/overwritten by the agent's hello.
ALTER TABLE hosts ADD COLUMN platform text NOT NULL DEFAULT 'windows';
