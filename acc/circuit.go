package main

import (
	"bytes"
	"fmt"
	"math/big"

	"github.com/consensys/gnark-crypto/accumulator/merkletree"
	"github.com/consensys/gnark-crypto/ecc"
	"github.com/consensys/gnark-crypto/hash"
	"github.com/consensys/gnark/backend/witness"
	"github.com/consensys/gnark/frontend"
	"github.com/consensys/gnark/std/algebra/sw_bls12377"
	"github.com/consensys/gnark/std/commitments/kzg_bls12377"
	"github.com/consensys/gnark/std/hash/mimc"
	"github.com/consensys/gnark/std/math/bits"
)

var curveID = ecc.BW6_761
var hashID = hash.MIMC_BW6_761

const (
	InputSize = 2
	Depth     = 5
)

var (
	numNodes = 1 << (Depth)
)

type Circuit struct {
	MerkleProofs [InputSize]MerkleCircuit
	Commitments  [InputSize]sw_bls12377.G1Affine
	Proof        kzg_bls12377.OpeningProof
	Random       frontend.Variable `gnark:",public"`
	VerifyKey    kzg_bls12377.VK   `gnark:",public"`
	MerkleRoot   frontend.Variable `gnark:",public"`
}

func (circuit *Circuit) Define(api frontend.API) error {
	h, err := mimc.NewMiMC(api)
	if err != nil {
		return err
	}

	rnd := frontend.Variable(circuit.Random)
	for i := 0; i < InputSize; i++ {
		h.Reset()
		h.Write(rnd)
		rnd = h.Sum()
		rndbit := bits.ToBinary(api, rnd)
		d := bits.FromBinary(api, rndbit[:Depth])
		api.AssertIsEqual(circuit.MerkleProofs[i].Leaf, d)

		h.Reset()
		h.Write(circuit.Commitments[i].X)
		h.Write(circuit.Commitments[i].Y)

		api.AssertIsEqual(circuit.MerkleProofs[i].Path[0], h.Sum())

		circuit.MerkleProofs[i].VerifyProof(api, &h, circuit.MerkleRoot)
		if i != 0 {
			circuit.Commitments[0].AddAssign(api, circuit.Commitments[i])
		}
	}

	kzg_bls12377.Verify(api, circuit.Commitments[0], circuit.Proof, circuit.Random, circuit.VerifyKey)
	return nil
}

func GenWithness() (witness.Witness, error) {
	var assignment Circuit
	pk, err := GenKey()
	if err != nil {
		return nil, err
	}

	assignment.VerifyKey.G1.Assign(&pk.SRS.G1[0])
	assignment.VerifyKey.G2[0].Assign(&pk.SRS.G2[0])
	assignment.VerifyKey.G2[1].Assign(&pk.SRS.G2[1])

	mod := curveID.ScalarField()
	fieldSize := len(mod.Bytes())

	fmt.Printf("node count: %d, field size %d\n", numNodes, fieldSize)

	comData := make([]byte, 0, numNodes*fieldSize)
	coms := make([]G1, numNodes)
	pfs := make([]Proof, numNodes)

	var rndfr Fr
	rndfr.SetRandom()
	rndBig := new(big.Int)
	rndfr.BigInt(rndBig)
	assignment.Random = rndBig

	h := hashID.New()

	var accProof Proof
	for i := 0; i < int(numNodes); i++ {
		data := genRandom(1 * MaxFileSize)
		com, err := pk.Commitment(data)
		if err != nil {
			return nil, err
		}

		pf, err := pk.Open(rndfr, data)
		if err != nil {
			return nil, err
		}

		err = pk.Verify(rndfr, com, pf)
		if err != nil {
			return nil, err
		}
		pfs[i] = pf
		coms[i].Set(&com)

		h.Reset()
		h.Write(com.X.Marshal())
		h.Write(com.Y.Marshal())

		comData = append(comData, h.Sum(nil)...)
	}

	var accCom G1
	rnd := new(big.Int).Set(rndBig)
	max := new(big.Int).SetUint64(1<<Depth - 1)
	for i := 0; i < InputSize; i++ {
		h.Reset()
		var rbuf bytes.Buffer
		rbuf.Write(make([]byte, fieldSize-len(rnd.Bytes())))
		rbuf.Write(rnd.Bytes())
		h.Write(rbuf.Bytes())
		sum := h.Sum(nil)
		rnd.SetBytes(sum)

		choosed := new(big.Int).And(rnd, max)
		pindex := choosed.Uint64()
		fmt.Printf("choose point %d %d \n", i, pindex)

		assignment.Commitments[i].Assign(&coms[pindex])
		accCom.Add(&accCom, &coms[pindex])

		accProof.ClaimedValue.Add(&accProof.ClaimedValue, &pfs[pindex].ClaimedValue)
		accProof.H.Add(&accProof.H, &pfs[pindex].H)

		buf := bytes.NewBuffer(comData)
		merkleRoot, merkleProof, numLeaves, err := merkletree.BuildReaderProof(buf, hashID.New(), fieldSize, pindex)
		if err != nil {
			return nil, err
		}

		fmt.Printf("merkle index %d, depth %d leaf %d, root %d\n", pindex, len(merkleProof), len(merkleProof[0]), len(merkleRoot))

		verified := merkletree.VerifyProof(hashID.New(), merkleRoot, merkleProof, pindex, numLeaves)
		if !verified {
			return nil, fmt.Errorf("invalid merkle proof")
		}

		assignment.MerkleRoot = merkleRoot
		assignment.MerkleProofs[i].Leaf = pindex
		for j := 0; j < Depth+1; j++ {
			assignment.MerkleProofs[i].Path[j] = merkleProof[j]
		}
	}

	err = pk.Verify(rndfr, accCom, accProof)
	if err != nil {
		return nil, err
	}

	claimBig := new(big.Int)
	accProof.ClaimedValue.BigInt(claimBig)
	assignment.Proof.ClaimedValue = claimBig
	assignment.Proof.H.Assign(&accProof.H)

	witness, err := frontend.NewWitness(&assignment, curveID.ScalarField())
	if err != nil {
		return nil, err
	}

	return witness, nil
}
