package ledger

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// AddPasskey stores a registered credential (public key + counter) for a user.
func (s *Store) AddPasskey(ctx context.Context, tenant, user, credID, credJSON string) error {
	return s.tx(ctx, tenant, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO passkeys (tenant_id, user_id, credential_id, credential) VALUES ($1,$2,$3,$4)`,
			tenant, user, credID, credJSON); err != nil {
			return err
		}
		return appendAudit(ctx, tx, tenant, "", "passkey.registered", map[string]any{"user": user, "credential": credID})
	})
}

// Passkeys returns a user's credentials as stored JSON.
func (s *Store) Passkeys(ctx context.Context, tenant, user string) ([]string, error) {
	var out []string
	err := s.tx(ctx, tenant, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT credential FROM passkeys WHERE user_id = $1 ORDER BY created_at`, user)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// UpdatePasskey saves a credential after use (new sign counter), scoped to its owner.
func (s *Store) UpdatePasskey(ctx context.Context, tenant, user, credID, credJSON string) error {
	return s.tx(ctx, tenant, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE passkeys SET credential = $3, last_used_at = now() WHERE user_id = $1 AND credential_id = $2`,
			user, credID, credJSON)
		return err
	})
}
