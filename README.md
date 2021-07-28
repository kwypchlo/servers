# Servers

This tool announces the current host to the world by publishing its name and ip
via a skylink v2. The skylink contains a JSON array with the list of all hosts
who are announcing themselves with the same credentials set. Each host will scan
the list for outdated entries and prune them.

The tool relies on the following environment variables:
* SIA_API_PASSWORD: the api password of the skyd node we use to communicate to skynet
* PORTAL_NAME: the name of the host, e.g. dev1.siasky.dev
* SERVERLIST_ENTROPY: 32 bytes of entropy in hex encoding, used to derive the public and secret keys used to access the v2 skylink
* SERVERLIST_TWEAK: 32 bytes of data in hex encoding

I use [ipify.org](https://www.ipify.org/) to determine the local IP.
