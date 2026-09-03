-- Financial records must be archived and independently verified before a downgrade.
-- Intentionally non-destructive: older application releases ignore these tables.
SELECT 1; -- Preserve financial history when rolling the application back.
