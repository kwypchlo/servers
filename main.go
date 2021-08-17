package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"math"
	"math/rand"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/ro-tex/skydb"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/NebulousLabs/fastrand"
	"gitlab.com/SkynetLabs/skyd/node/api/client"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/types"
)

type (
	// config holds the entire configuration of the tool:
	// * Entropy and Tweak are the parameters used to access the correct record
	// in SkyDB. These should be the same on all machines who want to appear on
	// the same list.
	// * OwnName is the name of the server in the list, e.g. dev1.siasky.dev.
	// * SkydAddress is the IP:PORT combination on which we can talk to the
	// local skyd.
	// * SkydApiPassword is the API password fo the local skyd.
	config struct {
		Entropy         [32]byte
		Tweak           [32]byte
		OwnName         string
		SkydAddress     string
		SkydApiPassword string
	}

	// server describes the information we collect for each server on the list.
	server struct {
		Name         string    `json:"name"`
		IP           string    `json:"ip"`
		LastAnnounce time.Time `json:"last_announce"`
	}
)

// getServerList loads the server list from SkyDB.
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
	fmt.Printf("got %d: %v\n", rev, servers)
	return servers, rev, nil
}

// putServerList stores the server list in SkyDB.
func putServerList(db *skydb.SkyDB, list []server, tweak [32]byte, rev uint64) error {
	data, err := json.Marshal(list)
	if err != nil {
		return errors.AddContext(err, "failed to marshal server list")
	}
	err = db.Write(data, tweak, rev)
	if err != nil {
		return errors.AddContext(err, "failed to write to skydb")
	}
	fmt.Printf("put %d: %v\n", rev, list)
	return nil
}

// updateOwnRecord adds our information to the list, removing the existing entry
// if it exists. If the server has multiple IP addresses, the address in the
// list might change between executions.
func updateOwnRecord(list []server, ownName string) ([]server, error) {
	ip, err := getOwnIP()
	if err != nil {
		// The IP is not critical to the operation of the tool, so we will just
		// skip setting it.
		fmt.Println(errors.AddContext(err, "failed to get own ip").Error())
		ip = ""
	}
	for i := range list {
		if list[i].Name == ownName {
			if ip != "" {
				list[i].IP = ip
			}
			list[i].LastAnnounce = time.Now()
			return list, nil
		}
	}
	self := server{
		Name:         ownName,
		IP:           ip,
		LastAnnounce: time.Now(),
	}
	return append(list, self), nil
}

// removeOutdatedEntries prunes all entries in the list that haven't been
// updated in 7 days.
func removeOutdatedEntries(list []server) []server {
	cutoff := time.Now().AddDate(0, 0, -7)
	var updatedList []server
	for _, s := range list {
		if s.LastAnnounce.After(cutoff) {
			updatedList = append(updatedList, s)
		}
	}
	return updatedList
}

// getConfig reads all the configuration data for the service. This data comes
// mostly from environment variables.
func getConfig() (config, error) {
	cfg := config{}

	ownName := os.Getenv("SKYNET_SERVER_API")
	if ownName == "" {
		return config{}, errors.New("failed to get own name. is SKYNET_SERVER_API env var defined?")
	}
	cfg.OwnName = strings.TrimPrefix(strings.TrimPrefix(ownName, "http://"), "https://")

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

// getOwnIP uses an external service in order to discover our external IP.
func getOwnIP() (string, error) {
	resp, err := http.Get("https://api.ipify.org")
	if err != nil || resp.StatusCode != http.StatusOK {
		return "", errors.AddContext(err, "failed to query api.ipify.org")
	}
	defer resp.Body.Close()
	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", errors.AddContext(err, "failed to read api.ipify.org response")
	}
	ip := string(bodyBytes)
	// This regex only detects IPv4. We might need to expand it in the future,
	// so it supports IPv6 as well.
	match, err := regexp.MatchString("^\\d{1,3}\\.\\d{1,3}\\.\\d{1,3}\\.\\d{1,3}$", ip)
	if err != nil || !match {
		msg := fmt.Sprintf("invalid ip received '%s'", ip)
		return "", errors.AddContext(err, msg)
	}
	return ip, nil
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
	isRetryRun := false
	for {
		if isRetryRun {
			// sleep between 0 and 3 minutes to allow other servers to finish their
			// updates without running into a series of races
			rand.Seed(int64(fastrand.Uint64n(math.MaxInt64)))
			sleepDur := time.Duration(rand.Intn(3*60)) * time.Second
			fmt.Printf("update was unsuccessful. sleeping for %d seconds.\n", sleepDur/time.Second)
			time.Sleep(sleepDur)
		}
		list, rev, err := getServerList(db, cfg.Tweak)
		if err != nil {
			fmt.Println(errors.AddContext(err, "failed to get server list"))
			isRetryRun = true
			continue
		}
		updatedList, err := updateOwnRecord(list, cfg.OwnName)
		if err != nil {
			fmt.Println(errors.AddContext(err, "failed to update list"))
			isRetryRun = true
			continue
		}
		cleanList := removeOutdatedEntries(updatedList)
		err = putServerList(db, cleanList, cfg.Tweak, rev+1)
		if err != nil {
			fmt.Println(errors.AddContext(err, "failed to update server list"))
			isRetryRun = true
			continue
		}
		// We want to sleep here for a bit in order to give the system time to
		// stabilize, otherwise we can run into a race where two machines write
		// different data for the same revision and both get positive responses
		// but only one of them gets selected as winner and gets their data
		// persisted.
		time.Sleep(3 * time.Second)
		if !checkSuccess(db, cfg.Tweak, cfg.OwnName) {
			fmt.Println("success check failed")
			isRetryRun = true
			continue
		}
		break
	}

	// output the skylink. this serves as a confirmation of a successful run and
	// as a handy way to get the skylink.
	sl := skymodules.NewSkylinkV2(types.Ed25519PublicKey(pk), cfg.Tweak)
	fmt.Printf("skylink updated successfully: %s\n", sl.String())
}
