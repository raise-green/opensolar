package core

import (
	"log"

	"github.com/pkg/errors"

	utils "github.com/Varunram/essentials/utils"
	xlm "github.com/Varunram/essentials/xlm"
	assets "github.com/Varunram/essentials/xlm/assets"
	escrow "github.com/Varunram/essentials/xlm/escrow"
	wallet "github.com/Varunram/essentials/xlm/wallet"
	consts "github.com/YaleOpenLab/opensolar/consts"
)

// RequestWaterfallWithdrawal requests withdrawal of funds from the escrow account
func RequestWaterfallWithdrawal(entityIndex int, projIndex int, amount float64) error {
	entity, err := RetrieveEntity(entityIndex)
	if err != nil {
		log.Println(err)
		return err
	}

	project, err := RetrieveProject(projIndex)
	if err != nil {
		log.Println(err)
		return err
	}

	var valid bool

	for key, elem := range project.WaterfallMap {
		if key == entity.U.Name {
			log.Println("developer name found in waterfall list")
			if elem < amount {
				log.Println("amount requested greater than that available, quitting")
				return errors.New("amount requested greater than that available, quitting")
			}
			log.Println("requesting transfer of: ", amount, " to the user from the escrow account")
			valid = true
		}
	}

	if !valid {
		return errors.New("developer not found")
	}

	if project.OneTimeUnlock == "" {
		log.Println("one time unlock not set, can't withdraw funds")
		return errors.New("one time unlock not set, can't withdraw funds")
	}

	if project.AdminFlagged {
		log.Println("project: ", projIndex, " has been flagged by admin")
		return errors.New("project flagged, can't withdraw")
	}

	if project.WaterfallMap == nil {
		project.WaterfallMap = make(map[string]float64)
		return errors.New("waterfall map not initiated, no withdrawals as a result")
	}

	recipient, err := RetrieveRecipient(project.RecipientIndex)
	if err != nil {
		log.Println(err)
		return err
	}

	recpSeed, err := wallet.DecryptSeed(recipient.U.StellarWallet.EncryptedSeed, project.OneTimeUnlock)
	if err != nil {
		log.Println(err)
		return errors.Wrap(err, "error while decrpyting seed")
	}

	if consts.Mainnet {
		susdbalancex := xlm.GetAssetBalance(project.EscrowPubkey, consts.StablecoinCode)
		susdbalance, err := utils.ToFloat(susdbalancex)
		if err != nil {
			log.Println(err)
			return err
		}

		if susdbalance < amount {
			log.Println("sufficient amount not available in escrow, not transferring funds")
			return errors.New("sufficient amount not available in escrow, not transferring funds")
		}

		// we do have the required amount of funds, trust asset from developer's end and transfer funds
		_, err = assets.TrustAsset(consts.StablecoinCode, consts.PlatformPublicKey, amount*2, recpSeed)
		if err != nil {
			return errors.Wrap(err, "Error while trusting debt asset")
		}

		err = escrow.SendAssetsFromEscrow(project.EscrowPubkey, entity.U.StellarWallet.PublicKey, recpSeed, consts.PlatformSeed, amount, "withdrawal", consts.StablecoinCode)
		if err != nil {
			log.Println(err)
			return err
		}
	} else {
		usdbalancex := xlm.GetAssetBalance(project.EscrowPubkey, consts.AnchorUSDCode)
		usdbalance, err := utils.ToFloat(usdbalancex)
		if err != nil {
			log.Println(err)
			return err
		}

		if usdbalance < amount {
			log.Println("sufficient amount not available in escrow, not transferring funds")
			return errors.New("sufficient amount not available in escrow, not transferring funds")
		}

		_, err = assets.TrustAsset(consts.AnchorUSDCode, consts.AnchorUSDAddress, amount*2, recpSeed)
		if err != nil {
			return errors.Wrap(err, "Error while trusting debt asset")
		}

		err = escrow.SendAssetsFromEscrow(project.EscrowPubkey, entity.U.StellarWallet.PublicKey, recpSeed, consts.PlatformSeed, amount, "withdrawal", consts.AnchorUSDCode)
		if err != nil {
			log.Println(err)
			return err
		}
	}

	return nil
}
