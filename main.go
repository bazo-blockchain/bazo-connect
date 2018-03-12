package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/rand"
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
	"strconv"
	"time"
)

var (
	logger    *log.Logger
	openAcc   = make(map[int][64]byte)
	openFunds = make(map[int][64]byte)
)

const (
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
	logger = log.New(os.Stdout, "INFO: ", log.Ldate|log.Ltime|log.Lshortfile)

	go checkStatus()
	go processNewAccRoutine()
	processNewFundsRoutine()
}

func checkStatus() {
	for {
		time.Sleep(10 * time.Second)

		logger.Println("Status:")
		fmt.Println("Open accounts (address):")
		for _, address := range openAcc {
			fmt.Printf("%x\n", address)
		}

		fmt.Println("Open funds (address):")
		for _, address := range openFunds {
			fmt.Printf("%x\n", address)
		}
	}
}
func processNewAccRoutine() {
	for {
		time.Sleep(30 * time.Second)

		if err := processNewAcc(); err != nil {
			logger.Printf("Processing new accounts failed: %v", err)
		}
	}
}

func processNewFundsRoutine() {
	for {
		time.Sleep(30 * time.Second)

		if err := processNewFunds(); err != nil {
			logger.Printf("Processing new funds failed: %v", err)
		}
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
			txHash, err := createAccTx(address)
			if err != nil {
				return err
			}

			sig, err := signTx(txHash)
			if err != nil {
				return err
			}

			if err := sendTx(txHash, sig, "accTx"); err != nil {
				return err
			}

			setStatus(id, "pending")
			delete(openAcc, id)
		}
	}

	return nil
}

func processNewFunds() (err error) {
	if err = reqCarmaSummary("fundprocessed"); err != nil {
		return err
	}

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

			txHash, err := createFundsTx(address, status.Amount)
			if err != nil {
				return err
			}

			sig, err := signTx(txHash)
			if err != nil {
				return err
			}

			err = sendTx(txHash, sig, "fundsTx")
			if err != nil {
				return err
			}

			setStatus(id, "processed")
			delete(openFunds, id)
		} else {
			setStatus(id, "open")
			openAcc[id] = address
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
			if o.Status == status && len(o.Public_key) == 128 {
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

	return nil
}

func reqAccount(address [64]byte) (acc *client.Account, err error) {
	response, err := http.Get("http://" + client.LIGHT_CLIENT_SERVER + "/account/" + hex.EncodeToString(address[:]))
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

func createAccTx(address [64]byte) (txHash [32]byte, err error) {
	pubKey, _, err := storage.ExtractKeyFromFile(os.Args[1])
	issuer := storage.GetAddressFromPubKey(&pubKey)

	var contents []REST.Content
	jsonResponse := REST.JsonResponse{Content: contents}
	jsonValue, _ := json.Marshal(jsonResponse)

	response, err := http.Post("http://"+client.LIGHT_CLIENT_SERVER+"/createAccTx/"+hex.EncodeToString(address[:])+"/0/1/"+hex.EncodeToString(issuer[:]), "application/json", bytes.NewBuffer(jsonValue))
	if err != nil {
		return txHash, errors.New(fmt.Sprintf("The HTTP request failed with error %s", err))
	}

	data, _ := ioutil.ReadAll(response.Body)

	json.Unmarshal([]byte(data), &jsonResponse)

	if jsonResponse.Code != 200 {
		return txHash, errors.New(fmt.Sprintf("Could not create tx. Error code: %v", jsonResponse.Code))
	}

	txHashInt, _ := new(big.Int).SetString(jsonResponse.Content[0].Detail.(string), 16)
	copy(txHash[:], txHashInt.Bytes())

	return txHash, err
}

func createFundsTx(address [64]byte, amount int) (txHash [32]byte, err error) {
	pubKey, _, err := storage.ExtractKeyFromFile(os.Args[1])
	issuer := storage.GetAddressFromPubKey(&pubKey)
	acc, _ := reqAccount(issuer)

	var contents []REST.Content
	jsonResponse := REST.JsonResponse{Content: contents}
	jsonValue, _ := json.Marshal(jsonResponse)

	response, err := http.Post("http://"+client.LIGHT_CLIENT_SERVER+"/createFundsTx/0/"+strconv.Itoa(amount)+"/1/"+fmt.Sprint(acc.TxCnt)+"/"+hex.EncodeToString(issuer[:])+"/"+hex.EncodeToString(address[:]), "application/json", bytes.NewBuffer(jsonValue))
	if err != nil {
		return txHash, errors.New(fmt.Sprintf("The HTTP request failed with error %s", err))
	}

	data, _ := ioutil.ReadAll(response.Body)

	json.Unmarshal([]byte(data), &jsonResponse)

	if jsonResponse.Code != 200 {
		return txHash, errors.New(fmt.Sprintf("Could not create tx. Error code: %v", jsonResponse.Code))
	}

	txHashInt, _ := new(big.Int).SetString(jsonResponse.Content[0].Detail.(string), 16)
	copy(txHash[:], txHashInt.Bytes())

	return txHash, err
}

func sendTx(txHash [32]byte, sig [64]byte, txType string) (err error) {
	var response *http.Response
	var jsonResponse REST.JsonResponse
	jsonValue, _ := json.Marshal(jsonResponse)

	if txType == "accTx" {
		response, err = http.Post("http://"+client.LIGHT_CLIENT_SERVER+"/sendAccTx/"+hex.EncodeToString(txHash[:])+"/"+hex.EncodeToString(sig[:]), "application/json", bytes.NewBuffer(jsonValue))
	} else if txType == "fundsTx" {
		response, err = http.Post("http://"+client.LIGHT_CLIENT_SERVER+"/sendFundsTx/"+hex.EncodeToString(txHash[:])+"/"+hex.EncodeToString(sig[:]), "application/json", bytes.NewBuffer(jsonValue))

	}
	if err != nil {
		return errors.New(fmt.Sprintf("The HTTP request failed with error %s", err))
	}

	data, _ := ioutil.ReadAll(response.Body)

	json.Unmarshal([]byte(data), &jsonResponse)

	if jsonResponse.Code != 200 {
		return errors.New(fmt.Sprintf("Could not send tx. Error code: %v", jsonResponse.Code))
	}

	return nil
}

func signTx(txHash [32]byte) (sig [64]byte, err error) {
	_, privKey, err := storage.ExtractKeyFromFile(os.Args[1])

	r, s, err := ecdsa.Sign(rand.Reader, &privKey, txHash[:])
	if err != nil {
		return sig, errors.New(fmt.Sprintf("Could not sign tx: %v", err))
	}

	copy(sig[32-len(r.Bytes()):32], r.Bytes())
	copy(sig[64-len(s.Bytes()):], s.Bytes())

	return sig, err
}
