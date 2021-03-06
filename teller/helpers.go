package main

import (
	"bufio"
	//"bytes"
	"encoding/json"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/pkg/errors"

	ipfs "github.com/Varunram/essentials/ipfs"
	utils "github.com/Varunram/essentials/utils"
	//	rpc "github.com/YaleOpenLab/openx/rpc"
	consts "github.com/YaleOpenLab/opensolar/consts"
	oracle "github.com/YaleOpenLab/opensolar/oracle"
)

// refreshLogin runs once every 5 minutes in order to fetch the latest recipient details
func refreshLogin(username string, pwhash string) error {
	var err error
	for {
		time.Sleep(consts.TellerPollInterval)
		err = login(username, pwhash)
		if err != nil {
			log.Println(err)
		}
	}
}

// EndHandler runs when the teller shuts down. Records the start time and location of the
// device in ipfs and commits it as two transactions to the Stellar blockchain
func endHandler() error {
	log.Println("Gracefully shutting down, please do not press any button in the process")
	var err error

	NowHash, err = getLatestBlockHash()
	if err != nil {
		log.Println(err)
	}

	hashString := "Device Shutting down. Info: " + DeviceInfo + " Device Location: " + DeviceLocation +
		" Device Unique ID: " + DeviceId + " " + "Start hash: " + StartHash + " Now hash: " + NowHash +
		"Ipfs HashChainHeader: " + HashChainHeader
	// note that we don't commit the latest hash chain header's hash here because this gives us a tighter timeline
	// to audit what really happened
	ipfsHash, err := ipfs.IpfsAddString(hashString)
	if err != nil {
		log.Println(err)
	}
	memo := "IPFSHASH: " + ipfsHash

	tx1, tx2, err := splitAndSend2Tx(memo)
	if err != nil {
		log.Fatal("could not split and send 2tx: ", err)
	}

	err = sendDeviceShutdownEmail(tx1, tx2)
	if err != nil {
		log.Fatal("could not send device shutdown email: ", err)
	}

	log.Println("sent device shutdown notice")
	commitDataShutdown()
	// save last known state of the system in the recipient's list of known hashes. Call this
	// last since there would still be data that we want to measure when the above commands are running
	return nil
	// have a return because we don't want to sigint while we send emails and stuff
}

func splitAndSend2Tx(memo string) (string, string, error) {
	// 10 padding chars + 46 (ipfs hash length) characters
	firstHalf := memo[:28]
	secondHalf := memo[28:]
	tx1, err := sendXLM(LocalRecipient.U.StellarWallet.PublicKey, 1, firstHalf)
	if err != nil {
		return "", "", err
	}
	time.Sleep(2 * time.Second)
	tx2, err := sendXLM(LocalRecipient.U.StellarWallet.PublicKey, 1, secondHalf)
	if err != nil {
		return "", "", err
	}
	log.Printf("tx hash: %s, tx2 hash: %s", tx1, tx2)
	return tx1, tx2, nil
}

func checkPayback() {
	for {
		log.Println("Paybck interval reached. Paying back automatically")
		assetName := LocalProject.DebtAssetCode
		amount := oracle.MonthlyBill() // TODO: consumption data must be accumulated from zigbee in the future

		err := projectPayback(assetName, amount)
		if err != nil {
			log.Println("Error while paying amount back", err)
			sendDevicePaybackFailedEmail()
		}
		time.Sleep(time.Duration(LocalProject.PaybackPeriod) * consts.OneWeekInSecond)
	}
}

// updateState stores the current state of the teller in ipfs and commits the ipfs hash to the blockchain
func updateState(trigger bool) {
	for {
		data, err := ioutil.ReadFile("data.txt")
		if err != nil {
			log.Println("error while trying to read data file")
			time.Sleep(consts.TellerPollInterval)
		}
		subcommand := string(data)
		// TODO: replace this with real data rather than fake data that we have here
		ipfsHash, err := ipfs.IpfsAddString("Device ID: " + DeviceId + " UPDATESTATE" + subcommand)
		if err != nil {
			log.Println("Error while fetching ipfs hash", err)
			time.Sleep(consts.TellerPollInterval)
		}

		ipfsHash = "STATUPD: " + ipfsHash
		// Stellar allows one to send as many stroops as desired to the same account, so
		// send timestamp stroops to ourselves.

		// memo field restricted to 28 bytes - AAAAAAAAAAAAAAAAAAAAAAAAAAAA

		// don't use platform RPCs for interacting with the blockchain

		hash1, err := sendXLM(LocalRecipient.U.StellarWallet.PublicKey, float64(utils.Unix()), ipfsHash[:28])
		if err != nil {
			log.Println(err)
		}

		hash2, err := sendXLM(LocalRecipient.U.StellarWallet.PublicKey, float64(utils.Unix()), ipfsHash[29:])
		if err != nil {
			log.Println(err)
		}

		// we updated state as hash1 and hash2
		colorOutput("Updated State: "+hash1+" "+hash2, MagentaColor)
		if trigger {
			break // we trigerred this manually, don't want to keep doing this
		}
		time.Sleep(consts.TellerPollInterval)
	}
}

// storeDataLocal stores the data we observe in real time to a file
func storeDataLocal() {
	log.Println("storing a local copy of data")
	path := consts.TellerHomeDir + "/data.txt"

	transport := &http.Transport{
		MaxIdleConns:       10,
		IdleConnTimeout:    30 * time.Second,
		DisableCompression: true,
	}
	client := &http.Client{Transport: transport}

	body := "https://api.particle.io/v1/devices/events?access_token=3f7d69aa99956fd77c5466f3f52eb6132f500210"
	resp, err := client.Get(body)
	if err != nil {
		log.Println("error while reading from streaming endpoint: ", err)
		return
	}

	defer func() {
		if ferr := resp.Body.Close(); ferr != nil {
			err = ferr
		}
	}()

	reader := bufio.NewReader(resp.Body)
	x := make([]byte, 200)
	// open and write to file
	if _, err := os.Stat(path); os.IsNotExist(err) {
		_, err = os.Create(path)
		if err != nil {
			log.Println("error while opening file", err)
			return
		}
	} else {
		data, err := ioutil.ReadFile(path)
		if err != nil {
			// don't start the teller if we can't read the last known hash since this would break continuity
			log.Println(err)
			return
		}
		HashChainHeader = string(data)
	}

	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, os.ModeAppend)
	if err != nil {
		log.Println("error while opening file", err)
		return
	}

	log.Println("starting to stream data from particle board: ")
	// this loop waits for inputs (in this case from the particle API) and continually
	// writes it to a data stream
	for {
		_, err = reader.Read(x)
		if err != nil {
			log.Println(err)
			continue
		}
		//log.Println("streaming data from particle board: ", string(x))
		_, err = file.Write(x)
		if err != nil {
			log.Println("error while writing to file", err)
			continue
		}
		size, err := file.Stat()
		if err != nil {
			log.Println(err)
			continue
		}

		// log.Println("File size is: ", size.Size())
		if size.Size() >= int64(consts.TellerMaxLocalStorageSize) {
			log.Println("flushing data to ipfs")
			// close the file, store in ipfs, get hash, delete file and create same file again
			// with the previous file's hash (so people can verify) as the first line
			err = file.Close()
			if err != nil {
				log.Println("couldn't close file, trying again")
				time.Sleep(2 * time.Second)
				continue
			}
			fileHash, err := ipfs.IpfsAddBytes([]byte(path))
			if err != nil {
				log.Println("Couldn't hash file: ", err)
			}
			HashChainHeader = fileHash
			fileHash = "IPFSHASHCHAIN: " + fileHash + "\n" // the header of the ipfs hashchain that we form
			// log.Println("HashChainHeader: ", HashChainHeader)
			os.Remove(path)
			_, err = os.Create(path)
			if err != nil {
				log.Println("error while opening file", err)
				time.Sleep(2 * time.Second)
				continue
			}
			file, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, os.ModeAppend)
			if err != nil {
				log.Println("error while opening file", err)
				time.Sleep(2 * time.Second)
				continue
			}
			file.Write([]byte(fileHash))
		}
	}
}

// commitDataShutdown is called when the teller errors out and goes down
func commitDataShutdown() {
	// retrieve data from local storage
	log.Println("printing data before shutdown")
	path := consts.TellerHomeDir + "/data.txt"

	fileHash, err := ipfs.IpfsAddBytes([]byte(path))
	if err != nil {
		log.Println("Couldn't hash file: ", err)
	}

	defer func() {
		if ferr := os.Remove(path); ferr != nil {
			ferr = err
		}
	}()

	_, err = os.Create(path)
	if err != nil {
		log.Println("error while opening file", err)
		return
	}

	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, os.ModeAppend)
	if err != nil {
		log.Println("error while opening file", err)
		return
	}

	file.Write([]byte(fileHash))
	err = file.Close()
	if err != nil {
		defer func() {
			if ferr := os.Remove(path); ferr != nil {
				ferr = err
			}
		}()
	}

	err = storeStateHistory(fileHash)
	if err != nil {
		return
	}
}

const tellerUrl = "https://localhost"

type statusResponse struct {
	Code   int
	Status string
}

// generateDeviceID generates a random 16 character device ID
func generateDeviceID() (string, error) {
	rs := utils.GetRandomString(16)
	upperCase := strings.ToUpper(rs)
	return upperCase, nil
}

// checkDeviceID checks the device's ID against a locally saved copy
func checkDeviceID() error {
	// checks whether we've set device id beforehand
	if _, err := os.Stat(consts.TellerHomeDir); os.IsNotExist(err) {
		// directory does not exist, create a device id
		log.Println("Creating home directory for teller")
		os.MkdirAll(consts.TellerHomeDir, os.ModePerm)
		path := consts.TellerHomeDir + "/deviceid.hex"
		file, err := os.Create(path)
		if err != nil {
			return errors.Wrap(err, "could not create device id file")
		}
		deviceId, err := generateDeviceID()
		if err != nil {
			return errors.Wrap(err, "could not generate device id")
		}
		colorOutput("GENERATED UNIQUE DEVICE ID: "+deviceId, GreenColor)
		_, err = file.Write([]byte(deviceId))
		if err != nil {
			return errors.Wrap(err, "could not write device id to file")
		}
		file.Close()
		err = setDeviceId(LocalRecipient.U.Username, deviceId)
		if err != nil {
			return errors.Wrap(err, "could not store device id in remote platform")
		}
	}
	return nil
}

// getDeviceID retrieves the deviceId from storage
func getDeviceID() (string, error) {
	path := consts.TellerHomeDir + "/deviceid.hex"
	file, err := os.Open(path)
	if err != nil {
		return "", errors.Wrap(err, "could not open teller home path")
	}

	defer func() {
		if ferr := file.Close(); ferr != nil {
			err = ferr
		}
	}()
	// read the hex string from the file
	data := make([]byte, 32)
	readBytes, err := file.Read(data)
	if err != nil {
		return "", errors.Wrap(err, "could not read from file")
	}
	if readBytes != 32 {
		return "", errors.New("length of strings doesn't match, quitting")
	}
	return string(data), nil
}

type energyStruct struct {
	EnergyTimestamp string `json:"energy_timestamp"`
	Unit            string `json:"unit"`
	Value           uint32 `json:"value"`
	OwnerId         string `json:"owner_id"`
	AssetId         string `json:"asset_id"`
}

func updateEnergyData() error {
	EnergyValue = 0

	origPath := "data.txt"
	hcPath := consts.TellerHomeDir + "/data.txt"

	presentData, err := ioutil.ReadFile(origPath)
	if err != nil {
		return errors.Wrap(err, "could not open data file for reading")
	}

	hc, err := os.OpenFile(hcPath, os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return errors.Wrap(err, "could not open data file for reading")
	}

	defer hc.Close()

	_, err = hc.Write(presentData)
	if err != nil {
		return errors.Wrap(err, "could not write data to the hc file")
	}

	// read size of the updated file
	hcData, err := ioutil.ReadFile(hcPath)
	if err != nil {
		return errors.Wrap(err, "could not open data file for reading")
	}

	for {
		size, err := hc.Stat()
		if err != nil {
			log.Println(err)
			break
		}
		log.Println("File size is: ", size.Size())
		if size.Size() >= int64(consts.TellerMaxLocalStorageSize) {
			log.Println("flushing data to ipfs")
			// close the file, store in ipfs, get hash, delete file and create same file again
			// with the previous file's hash (so people can verify) as the first line
			err = hc.Close()
			if err != nil {
				log.Println("couldn't close file, trying again")
				break
			}
			fileHash, err := ipfs.IpfsAddBytes(hcData)
			if err != nil {
				log.Println("Couldn't hash file: ", err)
			}
			HashChainHeader = fileHash
			fileHash = "IPFSHASHCHAIN: " + fileHash + "\n" // the header of the ipfs hashchain that we form
			// log.Println("HashChainHeader: ", HashChainHeader)
			os.Remove(hcPath)
			_, err = os.Create(hcPath)
			if err != nil {
				log.Println("error while opening file", err)
				continue
			}
			hc, err := os.OpenFile(hcPath, os.O_APPEND|os.O_WRONLY, os.ModeAppend)
			if err != nil {
				log.Println("error while opening file", err)
				continue
			}
			defer hc.Close()
			hc.Write([]byte(fileHash))
		}
		break
	}

	// now that the hash chain is done, take care of accumulating data
	f, err := os.Open(origPath)
	if err != nil {
		return errors.Wrap(err, "could not open data file for reading")
	}

	defer func() {
		if ferr := f.Close(); ferr != nil {
			err = ferr
		}
	}()

	reader := bufio.NewReader(f)

	for {
		var data []byte

		for i := 0; i < 7; i++ { // formatted according to the responses received from the lumen unit
			// which is further read by mosquitto_sub
			line, _, err := reader.ReadLine()
			if err != nil {
				log.Println("reached end of file")
				err = os.Remove("data.txt")
				if err != nil {
					return err
				}
				file, err := os.Create("data.txt")
				if err != nil {
					return err
				}
				err = file.Close()
				if err != nil {
					return err
				}
				return nil
			}
			data = append(data, line...)
		}

		var x energyStruct
		err = json.Unmarshal(data, &x)
		if err != nil {
			return errors.Wrap(err, "could not unmarshal json data struct")
		}

		EnergyValue += x.Value
	}
}

// readEnergyData reads energy data from a local file and stores it in the remote opensolar instance
func readEnergyData() {
	for {
		time.Sleep(LocalProject.PaybackPeriod * consts.OneWeekInSecond)
		log.Println("reading energy data from file")
		err := updateEnergyData()
		if err != nil {
			log.Println("error while reading energy data: ", err)
			continue
		}

		// need to update remote with the energy data
		log.Println("storing energy data on opensolar")
		data, err := putEnergy(EnergyValue)
		if err != nil {
			log.Println(err)
			continue
		}
		log.Println(string(data))
	}
}
