package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/lotus/api/v0api"
	"github.com/filecoin-project/lotus/build"
	"github.com/filecoin-project/lotus/chain/types/ethtypes"
	"github.com/urfave/cli/v2"
	"golang.org/x/xerrors"
	"google.golang.org/api/sheets/v4"
	"os"
	"strconv"
	"strings"

	verifregtypes9 "github.com/filecoin-project/go-state-types/builtin/v9/verifreg"
	"github.com/filecoin-project/lotus/chain/actors"
	"github.com/filecoin-project/lotus/chain/actors/builtin/verifreg"
	"github.com/filecoin-project/lotus/chain/types"
	lcli "github.com/filecoin-project/lotus/cli"
	logging "github.com/ipfs/go-log/v2"
	"google.golang.org/api/option"
)

var log = logging.Logger("main")

func addDataCap(ctx context.Context, api v0api.FullNode, from address.Address, inputAddress string, allowance types.BigInt) (*types.SignedMessage, error) {
	var filecoinAddress address.Address
	var decodeError error
	var err error

	if strings.HasPrefix(inputAddress, "0x") {
		ethAddress, err := ethtypes.ParseEthAddress(inputAddress)
		if err != nil {
			return nil, err
		}

		filecoinAddress, decodeError = ethAddress.ToFilecoinAddress()
	} else {
		filecoinAddress, decodeError = address.NewFromString(inputAddress)
	}

	if decodeError != nil {
		return nil, decodeError
	}
	if filecoinAddress == address.Undef {
		return nil, errors.New("invalid address")
	}
	var params []byte
	params, err = actors.SerializeParams(
		&verifregtypes9.AddVerifiedClientParams{
			Address:   filecoinAddress,
			Allowance: allowance,
		})
	if err != nil {
		return nil, err
	}

	var smsg *types.SignedMessage
	smsg, err = api.MpoolPushMessage(
		ctx, &types.Message{
			Params: params,
			From:   from,
			To:     verifreg.Address,
			Method: verifreg.Methods.AddVerifiedClient,
		}, nil)

	if err != nil {
		return nil, err
	}

	return smsg, err
}

func main() {
	logging.SetLogLevel("*", "INFO")

	log.Info("Starting fountain")

	local := []*cli.Command{
		runCmd,
	}

	app := &cli.App{
		Name:    "auto-notary",
		Usage:   "Devnet token distribution utility",
		Version: build.UserVersion(),
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "repo",
				EnvVars: []string{"LOTUS_PATH"},
				Value:   "~/.lotus", // TODO: Consider XDG_DATA_HOME
			},
		},

		Commands: local,
	}

	if err := app.Run(os.Args); err != nil {
		log.Warn(err)
		return
	}
}

var runCmd = &cli.Command{
	Name:  "run",
	Usage: "Start auto notary",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name: "from",
		},
		&cli.StringFlag{
			Name: "spreadsheetID",
		},
		&cli.StringFlag{
			Name: "credFile",
		},
	},
	Action: func(cctx *cli.Context) error {
		from, err := address.NewFromString(cctx.String("from"))
		if err != nil {
			return xerrors.Errorf("parsing source address (provide correct --from flag!): %w", err)
		}
		// Load the credentials file obtained from the Google Cloud Console
		// Replace "path/to/credentials.json" with the actual path to your credentials file
		credentialsFile := cctx.String("credFile")
		ctx := lcli.ReqContext(cctx)
		client, err := sheets.NewService(ctx, option.WithCredentialsFile(credentialsFile))
		if err != nil {
			log.Fatalf("Failed to create Google Sheets client: %v", err)
		}

		// Specify the spreadsheet ID and range to read from
		spreadsheetID := cctx.String("spreadsheetID")
		readRange := "Form Responses 1!A2:D"

		// Read values from the specified range in the spreadsheet
		response, err := client.Spreadsheets.Values.Get(spreadsheetID, readRange).Do()
		if err != nil {
			log.Fatalf("Failed to read from Google Sheets: %v", err)
		}

		// Process the response
		if len(response.Values) > 0 {

			api, closer, err := lcli.GetFullNodeAPI(cctx)
			if err != nil {
				return err
			}
			defer closer()
			ctx := lcli.ReqContext(cctx)

			for rowIndex, row := range response.Values {
				for _, cell := range row {
					log.Infof("%s", cell)
				}
				if len(row) > 3 {
					log.Info("skip")
					continue
				}
				fmt.Println()

				to := fmt.Sprintf("%s", row[1])
				allow := fmt.Sprintf("%s", row[2])
				ui64, _ := strconv.ParseUint(allow, 10, 64)
				allowance := types.NewInt(ui64 * 1024 * 1024)
				log.Infof("from:%v to:%v with %v", from, to, allowance)

				var updatedValue string
				smsg, err := addDataCap(ctx, api, from, to, allowance)
				if err != nil {
					updatedValue = err.Error()
				} else {
					// Modify the desired field on the current line
					updatedValue = smsg.Cid().String()
				}

				log.Infof("addDataCap return %v", updatedValue)

				//response.Values[rowIndex][3] = "updatedValue" // Modifying the third field (index 2) on each line

				// Write the modified value back to the spreadsheet for the current line
				writeRange := fmt.Sprintf("Form Responses 1!d%d", rowIndex+2) // Specify the range to write back for the current line
				updateRange := &sheets.ValueRange{
					Range:  writeRange,
					Values: [][]interface{}{{updatedValue}},
				}
				_, err = client.Spreadsheets.Values.Update(spreadsheetID, writeRange, updateRange).ValueInputOption("RAW").Do()
				if err != nil {
					log.Fatalf("Failed to update value in Google Sheets: %v", err)
				}
			}
		} else {
			fmt.Println("No data found.")
		}
		return nil
	},
}
