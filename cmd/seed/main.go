// Package main seeds the dev database with the canonical three users
// (Sohan, Aditi, Sayan) used in development and demo. The default
// password for each user is the user's phone number — convenient for
// dev only; production accounts go through the register flow.
//
// The seed runs in two passes:
//  1. Upsert each user's identity + bike profile + bio data.
//  2. Wire emergency contacts (Sohan ↔ Aditi) once both rows exist.
//
// Idempotent: re-running upserts on phone uniqueness, leaving prior
// password hashes in place. Safe to call any number of times.
//
// Usage:
//
//	conda activate RideAndConnect
//	make migrate-up        # ensure schema is current
//	make seed              # runs this command
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SohanBiswasSB/rideandconnect/internal/auth"
)

type seedUser struct {
	Phone           string
	Name            string
	Initials        string
	BikeModel       string
	BikeRole        string // "RIDER" or "PILLION"
	LicensePlate    string
	BloodType       string
	Age             int16
	HeightCm        int16
	WeightKg        int16
	TyreFrontSolo   float64
	TyreRearSolo    float64
	TyreFrontPillion float64
	TyreRearPillion  float64

	// Phone of the in-system emergency contact, if any. Resolved in pass 2.
	EmergencyContactPhone string
}

// users is the canonical seed list. Edit cautiously — these accounts are
// referenced in the demo flow described in docs/PRD.md and the README.
//
// Tyre pressures are illustrative dev defaults for the KTM 390-class
// machines (~ 33 PSI front / 36 PSI rear solo; rear pumped to ~38 PSI
// when carrying a pillion). Adjust to match each rider's manual.
var users = []seedUser{
	{
		Phone: "7501223507", Name: "Sohan Biswas", Initials: "SB",
		BikeModel: "KTM 390 X Plus", BikeRole: "RIDER", LicensePlate: "WB 52 K 7661",
		BloodType: "O_POS", Age: 28, HeightCm: 178, WeightKg: 72,
		TyreFrontSolo: 33.0, TyreRearSolo: 36.0,
		TyreFrontPillion: 33.0, TyreRearPillion: 38.0,
		EmergencyContactPhone: "8617792068", // Aditi
	},
	{
		Phone: "8617792068", Name: "Aditi Mukherjee", Initials: "AM",
		BikeModel: "KTM 390 X Plus", BikeRole: "PILLION", LicensePlate: "WB 52 K 7661",
		BloodType: "B_POS", Age: 27, HeightCm: 165, WeightKg: 56,
		TyreFrontSolo: 33.0, TyreRearSolo: 36.0,
		TyreFrontPillion: 33.0, TyreRearPillion: 38.0,
		EmergencyContactPhone: "7501223507", // Sohan
	},
	{
		Phone: "7003996450", Name: "Sayan Hazra", Initials: "SH",
		BikeModel: "KTM 390 S", BikeRole: "RIDER", LicensePlate: "23 BH 3379L",
		BloodType: "A_POS", Age: 29, HeightCm: 175, WeightKg: 70,
		TyreFrontSolo: 32.0, TyreRearSolo: 35.0,
		TyreFrontPillion: 32.0, TyreRearPillion: 37.0,
		// No emergency contact in the seed; user can set via updateProfile.
	},
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "seed: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping: %w", err)
	}

	// Pass 1: upsert each user.
	idsByPhone := make(map[string]uuid.UUID, len(users))
	for _, u := range users {
		hash, err := auth.HashPassword(u.Phone) // dev convenience: phone == password
		if err != nil {
			return fmt.Errorf("%s: hash: %w", u.Phone, err)
		}
		id, err := upsertUser(ctx, pool, u, hash)
		if err != nil {
			return fmt.Errorf("%s: upsert: %w", u.Phone, err)
		}
		idsByPhone[u.Phone] = id
		fmt.Printf("seeded user phone=%s name=%s role=%s plate=%s blood=%s\n",
			u.Phone, u.Name, u.BikeRole, strings.ToUpper(u.LicensePlate), u.BloodType)
	}

	// Pass 2: wire in-system emergency contacts now that all users exist.
	for _, u := range users {
		if u.EmergencyContactPhone == "" {
			continue
		}
		contactID, ok := idsByPhone[u.EmergencyContactPhone]
		if !ok {
			fmt.Printf("warn: %s emergency contact phone %s not in seed list; skipping\n", u.Phone, u.EmergencyContactPhone)
			continue
		}
		var contactName string
		if err := pool.QueryRow(ctx, `SELECT name FROM users WHERE id = $1`, contactID).Scan(&contactName); err != nil {
			return fmt.Errorf("read emergency contact name: %w", err)
		}
		if _, err := pool.Exec(ctx, `
			UPDATE users
			   SET emergency_contact_user_id = $2,
			       emergency_contact_name    = $3,
			       emergency_contact_phone   = $4
			 WHERE id = $1
		`, idsByPhone[u.Phone], contactID, contactName, u.EmergencyContactPhone); err != nil {
			return fmt.Errorf("set emergency contact: %w", err)
		}
		fmt.Printf("linked emergency contact: %s -> %s\n", u.Name, contactName)
	}

	fmt.Println("seed: done. Default password for each user is their phone number.")
	return nil
}

func upsertUser(ctx context.Context, pool *pgxpool.Pool, u seedUser, hash string) (uuid.UUID, error) {
	plate := strings.ToUpper(strings.TrimSpace(u.LicensePlate))
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return uuid.Nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var id uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO users (
			name, phone, password_hash,
			initials, bike_model, license_plate, default_bike_role,
			blood_type, age, height_cm, weight_kg,
			tyre_front_solo_psi, tyre_rear_solo_psi, tyre_front_pillion_psi, tyre_rear_pillion_psi
		)
		VALUES ($1, $2, $3,
		        $4, $5, $6, $7::bike_role,
		        $8::blood_type, $9, $10, $11,
		        $12, $13, $14, $15)
		ON CONFLICT (phone) DO UPDATE SET
			name                   = EXCLUDED.name,
			initials               = EXCLUDED.initials,
			bike_model             = EXCLUDED.bike_model,
			license_plate          = EXCLUDED.license_plate,
			default_bike_role      = EXCLUDED.default_bike_role,
			blood_type             = EXCLUDED.blood_type,
			age                    = EXCLUDED.age,
			height_cm              = EXCLUDED.height_cm,
			weight_kg              = EXCLUDED.weight_kg,
			tyre_front_solo_psi    = EXCLUDED.tyre_front_solo_psi,
			tyre_rear_solo_psi     = EXCLUDED.tyre_rear_solo_psi,
			tyre_front_pillion_psi = EXCLUDED.tyre_front_pillion_psi,
			tyre_rear_pillion_psi  = EXCLUDED.tyre_rear_pillion_psi
		RETURNING id
	`,
		u.Name, u.Phone, hash,
		u.Initials, u.BikeModel, plate, u.BikeRole,
		u.BloodType, u.Age, u.HeightCm, u.WeightKg,
		u.TyreFrontSolo, u.TyreRearSolo, u.TyreFrontPillion, u.TyreRearPillion,
	).Scan(&id)
	if err != nil {
		return uuid.Nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, err
	}
	return id, nil
}
