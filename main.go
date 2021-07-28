package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/ro-tex/skydb"
	"gitlab.com/NebulousLabs/errors"
	"gitlab.com/SkynetLabs/skyd/node/api/client"
	"gitlab.com/SkynetLabs/skyd/skymodules"
	"go.sia.tech/siad/crypto"
	"go.sia.tech/siad/types"
)

type (
	config struct {
		Entropy [32]byte
		Tweak   [32]byte
		OwnName string
	}

	server struct {
		Name         string    `json:"name"`
		IP           string    `json:"ip"`
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

func getOwnIP() (string, error) {
	resp, err := http.Get("https://api.ipify.org")
	if err != nil || resp.StatusCode != http.StatusOK {
		msg := fmt.Sprintf("failed to query api.ipify.org, status code %d", resp.StatusCode)
		return "", errors.AddContext(err, msg)
	}
	defer resp.Body.Close()
	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", errors.AddContext(err, "failed to read api.ipify.org response")
	}
	ip := string(bodyBytes)
	match, err := regexp.MatchString("^\\d{1,3}\\.\\d{1,3}\\.\\d{1,3}\\.\\d{1,3}$", ip)
	if err != nil || !match {
		msg := fmt.Sprintf("invalid ip received '%s'", ip)
		return "", errors.AddContext(err, msg)
	}
	return ip, nil
}

func updateOwnRecord(list []server, ownName string) ([]server, error) {
	ip, err := getOwnIP()
	if err != nil {
		return nil, errors.AddContext(err, "failed to get own ip")
	}
	for i := range list {
		if list[i].Name == ownName {
			list[i].IP = ip
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
		return config{}, errors.New("invalid SERVERLIST_TWEAK value")
	}
	copy(cfg.Tweak[:], bytes)

	return cfg, nil
}

func main() {
	cfg, err := getConfig()
	if err != nil {
		log.Fatal(errors.AddContext(err, "failed to read config"))
	}
	sk, pk := crypto.GenerateKeyPairDeterministic(cfg.Entropy)
	db, err := skydb.New(sk, pk, client.Options{})
	if err != nil {
		log.Fatal(errors.AddContext(err, "failed to get skydb instance"))
	}
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
}
