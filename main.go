package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/ro-tex/skydb"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/SkynetLabs/skyd/node/api/client"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/types"
)

type (
	config struct {
		Entropy         [32]byte
		Tweak           [32]byte
		OwnName         string
		SkydAddress     string
		SkydApiPassword string
	}

	server struct {
		Name         string    `json:"name"`
		LastAnnounce time.Time `json:"last_announce"`
	}
)

func getServerList(db *skydb.SkyDB, tweak [32]byte) ([]server, uint64, error) {
	b, rev, err := db.Read(tweak)
	if err != nil && strings.Contains(err.Error(), "skydb entry not found") {
		return []server{}, 0, nil
	}
	if err != nil {
		return nil, 0, errors.AddContext(err, "failed to read from skydb")
	}
	var servers []server
	err = json.Unmarshal(b, &servers)
	if err != nil {
		return nil, 0, errors.AddContext(err, "failed to unmarshal server list")
	}
	return servers, rev, nil
}

func putServerList(db *skydb.SkyDB, list []server, tweak [32]byte, rev uint64) error {
	data, err := json.Marshal(list)
	if err != nil {
		return errors.AddContext(err, "failed to marshal server list")
	}
	err = db.Write(data, tweak, rev+1)
	if err != nil {
		return errors.AddContext(err, "failed to write to skydb")
	}
	return nil
}

func updateOwnRecord(list []server, ownName string) ([]server, error) {
	for i := range list {
		if list[i].Name == ownName {
			list[i].LastAnnounce = time.Now()
			return list, nil
		}
	}
	self := server{
		Name:         ownName,
		LastAnnounce: time.Now(),
	}
	return append(list, self), nil
}

func removeOutdatedEntries(list []server) []server {
	cutoff := time.Now().AddDate(0, 0, -7)
	updatedList := []server{}
	for _, s := range list {
		if s.LastAnnounce.After(cutoff) {
			updatedList = append(updatedList, s)
		}
	}
	return updatedList
}

func getConfig() (config, error) {
	cfg := config{}

	ownName := os.Getenv("PORTAL_NAME")
	if ownName == "" {
		return config{}, errors.New("failed to get own name. is PORTAL_NAME env var defined?")
	}
	cfg.OwnName = ownName

	entropyStr := os.Getenv("SERVERLIST_ENTROPY")
	if entropyStr == "" {
		return config{}, errors.New("failed to get entropy. is SERVERLIST_ENTROPY env var defined?")
	}
	bytes, err := hex.DecodeString(entropyStr)
	if err != nil {
		return config{}, errors.AddContext(err, "invalid SERVERLIST_ENTROPY value")
	}
	copy(cfg.Entropy[:], bytes)

	tweakStr := os.Getenv("SERVERLIST_TWEAK")
	if ownName == "" {
		return config{}, errors.New("failed to get tweak. is SERVERLIST_TWEAK env var defined?")
	}
	bytes, err = hex.DecodeString(tweakStr)
	if err != nil {
		return config{}, errors.AddContext(err, "invalid SERVERLIST_TWEAK value")
	}
	copy(cfg.Tweak[:], bytes)

	cfg.SkydAddress = os.Getenv("SERVERLIST_SKYD")
	if cfg.SkydAddress == "" {
		cfg.SkydAddress = "localhost:9980"
	}

	cfg.SkydApiPassword = os.Getenv("SIA_API_PASSWORD")
	if cfg.SkydApiPassword == "" {
		return config{}, errors.New("failed to get api password. is SIA_API_PASSWORD env var defined?")
	}

	return cfg, nil
}

// checkSuccess fetches the list of servers and ensures that this server's
// record was updated within the last 5 minutes.
func checkSuccess(db *skydb.SkyDB, tweak [32]byte, ownName string) bool {
	list, _, err := getServerList(db, tweak)
	if err != nil {
		return false
	}
	for _, s := range list {
		if s.Name == ownName {
			return s.LastAnnounce.After(time.Now().Add(-5 * time.Minute))
		}
	}
	return false
}

func main() {
	err := godotenv.Load(os.Args[1])
	if err != nil {
		log.Fatal(errors.AddContext(err, "failed to load .env"))
	}
	cfg, err := getConfig()
	if err != nil {
		log.Fatal(errors.AddContext(err, "failed to read config"))
	}
	sk, pk := crypto.GenerateKeyPairDeterministic(cfg.Entropy)
	opts := client.Options{
		Address:   cfg.SkydAddress,
		Password:  cfg.SkydApiPassword,
		UserAgent: "Sia-Agent",
	}
	db, err := skydb.New(sk, pk, opts)
	if err != nil {
		log.Fatal(errors.AddContext(err, "failed to get skydb instance"))
	}

	// get the latest server list, update it and save it. then verify that we're
	// in the list with a recent record. if that's not true sleep for a while
	// and try again.
	for {
		list, rev, err := getServerList(db, cfg.Tweak)
		if err != nil {
			log.Fatal(errors.AddContext(err, "failed to get server list"))
		}
		updatedList, err := updateOwnRecord(list, cfg.OwnName)
		if err != nil {
			log.Fatal(errors.AddContext(err, "failed to update list"))
		}
		cleanList := removeOutdatedEntries(updatedList)
		err = putServerList(db, cleanList, cfg.Tweak, rev+1)
		if err != nil {
			log.Fatal(errors.AddContext(err, "failed to update server list"))
		}
		if checkSuccess(db, cfg.Tweak, cfg.OwnName) {
			break
		}
		// sleep between 0 and 3 minutes to allow other servers to finish their
		// updates without running into a series of races
		sleepDur := time.Duration(rand.Intn(3)*60) * time.Second
		fmt.Printf("update was unsuccessful. sleeping for %d seconds.\n", sleepDur/time.Second)
		time.Sleep(sleepDur)
	}

	// output the skylink. this serves as a confirmation of a successful run and
	// as a handy way to get the skylink.
	sl := skymodules.NewSkylinkV2(types.Ed25519PublicKey(pk), cfg.Tweak)
	fmt.Printf("skylink updated successfully: %s\n", sl.String())
}
