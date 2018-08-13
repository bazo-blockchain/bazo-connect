package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/bazo-blockchain/bazo-client/REST"
	"github.com/bazo-blockchain/bazo-client/client"
	"github.com/bazo-blockchain/bazo-miner/storage"
	"io/ioutil"
	"log"
	"math/big"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"time"
)

var (
	logger        *log.Logger
	issuer        string
	issuerAddress [64]byte
	multisig      string
	openAcc       = make(map[int][64]byte)
	openFunds     = make(map[int][64]byte)
)

const (
	//CLIENT = "oysyconnect.westeurope.cloudapp.azure.com"
	CARMAAPPID = "a63c1f72-8198-4a59-85be-67a96f87ab41"
	CARMAPOC   = "https://carma-poc.autoidlabs.ch/"
)

type carma_summary struct {
	Status    string      `json:"status"`
	Notices   interface{} `json:"notices"`
	Responses interface{} `json:"response"`
}

type carma_response struct {
	Id         int    `json:"id"`
	User_id    int    `json:"user_id,omitempty"`
	Public_key string `json:"public_key,omitempty"`
	Amount     int    `json:"amount,omitempty"`
	Status     string `json:"status,omitempty"`
	Max_amount int    `json:"max_amount,omitempty"`
	Token      string `json:"token,omitempty"`
	App_id     string `json:"app_id,omitempty"`
}

func main() {
	if len(os.Args) != 3 {
		log.Fatal("Usage bazo-connect <issuer> <multisig>")
	}

	issuer = os.Args[1]
	multisig = os.Args[2]

	issuerPubKey, _, _ := storage.ExtractKeyFromFile(issuer)
	issuerAddress = storage.GetAddressFromPubKey(&issuerPubKey)

	logger = storage.InitLogger()

	for {
		processNewAcc()
		processNewFunds()

		time.Sleep(30 * time.Second)
	}
}

func processNewAcc() (err error) {
	if err = reqCarmaSummary("open"); err != nil {
		return err
	}

	for id, address := range openAcc {
		acc, err := reqAccount(address)
		if err != nil {
			return err
		}

		if acc == nil {
			create(address)
		}

		setStatus(id, "pending")
		delete(openAcc, id)
	}

	return nil
}

func processNewFunds() (err error) {
	if err = reqCarmaSummary("fundprocessed"); err != nil {
		return err
	}

	issuerAcc, _ := reqAccount(issuerAddress)

	for id, address := range openFunds {
		acc, err := reqAccount(address)
		if err != nil {
			return err
		}

		if acc != nil {
			status, err := getStatus(id)
			if err != nil {
				return err
			}

			fund(issuerAcc, address, status.Amount)
			issuerAcc.TxCnt++
			setStatus(id, "processed")
			delete(openFunds, id)
		}
	}

	return nil
}

func reqCarmaSummary(status string) (err error) {
	response, err := http.Get(CARMAPOC + "bazo/summary?app_id=" + CARMAAPPID)
	if err != nil {
		return errors.New(fmt.Sprintf("The HTTP request failed with error %s", err))
	}

	data, _ := ioutil.ReadAll(response.Body)

	var responses []carma_response
	summary := carma_summary{Responses: &responses}
	json.Unmarshal([]byte(data), &summary)

	if summary.Status == "OK" {
		for _, o := range responses {
			if o.Status == status && len(o.Public_key) == 128 && o.Amount > 0 {
				var pubKey [64]byte
				pubKeyInt, _ := new(big.Int).SetString(o.Public_key, 16)
				copy(pubKey[:], pubKeyInt.Bytes())
				if status == "open" {
					openAcc[o.Id] = pubKey
				} else if status == "fundprocessed" {
					openFunds[o.Id] = pubKey
				}
			}
		}
	}

	logger.Println("Status:")
	fmt.Println("Open accounts (address)")
	for _, address := range openAcc {
		fmt.Printf("%x\n", address)
	}

	fmt.Println("Open funds (address)")
	for _, address := range openFunds {
		fmt.Printf("%x\n", address)
	}

	return nil
}

func reqAccount(address [64]byte) (acc *client.Account, err error) {
	response, err := http.Get("http://" + CLIENT + "/account/" + hex.EncodeToString(address[:]))
	if err != nil {
		return nil, errors.New(fmt.Sprintf("The HTTP request failed with error %s", err))
	}

	data, _ := ioutil.ReadAll(response.Body)

	var contents []REST.Content
	content := REST.Content{"account", &acc}
	contents = append(contents, content)
	jsonResponse := REST.JsonResponse{Content: contents}

	json.Unmarshal([]byte(data), &jsonResponse)

	return acc, nil
}

func getStatus(id int) (status carma_response, err error) {
	response, err := http.Get(CARMAPOC + "bazo/request_status?id=" + strconv.Itoa(id) + "&app_id=" + CARMAAPPID)
	if err != nil {
		return status, errors.New(fmt.Sprintf("The HTTP request failed with error %s", err))
	}

	data, _ := ioutil.ReadAll(response.Body)

	summary := carma_summary{Responses: &status}

	json.Unmarshal([]byte(data), &summary)

	return status, nil
}

func setStatus(id int, status string) error {
	req := carma_response{Id: id, App_id: CARMAAPPID, Status: status}
	jsonValue, _ := json.Marshal(req)

	response, err := http.Post(CARMAPOC+"bazo/update_request", "application/json", bytes.NewBuffer(jsonValue))
	if err != nil {
		return errors.New(fmt.Sprintf("The HTTP request failed with error %s", err))
	}

	data, _ := ioutil.ReadAll(response.Body)

	var responses []carma_response
	summary := carma_summary{Responses: responses}

	json.Unmarshal([]byte(data), &summary)

	if summary.Status != "OK" {
		return errors.New(fmt.Sprintf("Status update failed."))
	}

	return nil
}

func create(address [64]byte) {
	out, err := exec.Command("bazo-client", "accTx", "0", "1", issuer, hex.EncodeToString(address[:])).Output()
	if err != nil {
		log.Fatal(err)
	}

	logger.Printf("%s", out)
}

func fund(issuerAcc *client.Account, address [64]byte, amount int) {
	out, err := exec.Command("bazo-client", "fundsTx", "0", strconv.Itoa(amount), "1", strconv.Itoa(int(issuerAcc.TxCnt)), issuer, hex.EncodeToString(address[:]), multisig).Output()
	if err != nil {
		log.Fatal(err)
	}

	logger.Printf("%s", out)
}
