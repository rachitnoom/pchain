package crypto

import (
	"bytes"
	"fmt"

	. "github.com/tendermint/go-common"
	data "github.com/tendermint/go-data"
	"github.com/tendermint/go-wire"
	"github.com/Nik-U/pbc"
)

// Signature is a part of Txs and consensus Votes.
type Signature interface {
	Bytes() []byte
	IsZero() bool
	String() string
	Equals(Signature) bool
}

var sigMapper data.Mapper

// register both public key types with go-data (and thus go-wire)
func init() {
	sigMapper = data.NewMapper(SignatureS{}).
		RegisterImplementation(SignatureEd25519{}, NameEd25519, TypeEd25519).
		RegisterImplementation(SignatureSecp256k1{}, NameSecp256k1, TypeSecp256k1).
		RegisterImplementation(EtherumSignature{}, NameEtherum, TypeEtherum).
		RegisterImplementation(BLSSignature{}, NameBls, TypeBls)
}

// SignatureS add json serialization to Signature
type SignatureS struct {
	Signature
}

func WrapSignature(sig Signature) SignatureS {
	for ssig, ok := sig.(SignatureS); ok; ssig, ok = sig.(SignatureS) {
		sig = ssig.Signature
	}
	return SignatureS{sig}
}

func (p SignatureS) MarshalJSON() ([]byte, error) {
	return sigMapper.ToJSON(p.Signature)
}

func (p *SignatureS) UnmarshalJSON(data []byte) (err error) {
	parsed, err := sigMapper.FromJSON(data)
	if err == nil && parsed != nil {
		p.Signature = parsed.(Signature)
	}
	return
}

func (p SignatureS) Empty() bool {
	return p.Signature == nil
}

func SignatureFromBytes(sigBytes []byte) (sig Signature, err error) {
	err = wire.ReadBinaryBytes(sigBytes, &sig)
	return
}

//-------------------------------------

// Implements Signature
type SignatureEd25519 [64]byte

func (sig SignatureEd25519) Bytes() []byte {
	return wire.BinaryBytes(struct{ Signature }{sig})
}

func (sig SignatureEd25519) IsZero() bool { return len(sig) == 0 }

func (sig SignatureEd25519) String() string { return fmt.Sprintf("/%X.../", Fingerprint(sig[:])) }

func (sig SignatureEd25519) Equals(other Signature) bool {
	if otherEd, ok := other.(SignatureEd25519); ok {
		return bytes.Equal(sig[:], otherEd[:])
	} else {
		return false
	}
}

func (p SignatureEd25519) MarshalJSON() ([]byte, error) {
	return data.Encoder.Marshal(p[:])
}

func (p *SignatureEd25519) UnmarshalJSON(enc []byte) error {
	var ref []byte
	err := data.Encoder.Unmarshal(&ref, enc)
	copy(p[:], ref)
	return err
}

//-------------------------------------

// Implements Signature
type SignatureSecp256k1 []byte

func (sig SignatureSecp256k1) Bytes() []byte {
	return wire.BinaryBytes(struct{ Signature }{sig})
}

func (sig SignatureSecp256k1) IsZero() bool { return len(sig) == 0 }

func (sig SignatureSecp256k1) String() string { return fmt.Sprintf("/%X.../", Fingerprint(sig[:])) }

func (sig SignatureSecp256k1) Equals(other Signature) bool {
	if otherEd, ok := other.(SignatureSecp256k1); ok {
		return bytes.Equal(sig[:], otherEd[:])
	} else {
		return false
	}
}
func (p SignatureSecp256k1) MarshalJSON() ([]byte, error) {
	return data.Encoder.Marshal(p)
}

func (p *SignatureSecp256k1) UnmarshalJSON(enc []byte) error {
	return data.Encoder.Unmarshal((*[]byte)(p), enc)
}


type EtherumSignature []byte

func (sig EtherumSignature) SigByte() []byte {
	return sig[:]
}

func (sig EtherumSignature) Bytes() []byte {
	return wire.BinaryBytes(struct{ Signature }{sig})
}

func (sig EtherumSignature) IsZero() bool {
	return len(sig) == 0
}

func (sig EtherumSignature) String() string {
	return fmt.Sprintf("/%X.../", Fingerprint(sig[:]))
}

func (sig EtherumSignature) Equals(other Signature) bool {

	if otherEd, ok := other.(EtherumSignature); ok {
		return bytes.Equal(sig[:], otherEd[:])
	} else {
		return false
	}
}

func (sig EtherumSignature) MarshalJSON() ([]byte, error) {
	return data.Encoder.Marshal(sig[:])
}

func (sig *EtherumSignature) UnmarshalJSON(enc []byte) error {
	var ref []byte
	err := data.Encoder.Unmarshal(&ref, enc)
	*sig = make(EtherumSignature, len(ref))
	copy((*sig)[:], ref)
	return err
}


//-------------------------------------
// Implements Signature
type BLSSignature []byte

func CreateBLSSignature() BLSSignature {
	privKey := pairing.NewZr().Rand()
	return privKey.Bytes()
}

func (sig BLSSignature) getElement() *pbc.Element {
	return pairing.NewG2().SetBytes(sig)
}

func (sig BLSSignature) Set1() {
	sig.getElement().Set1()
}

func BLSSignatureMul(l, r Signature) Signature {
	lSign,lok := l.(BLSSignature);
	rSign, rok := r.(BLSSignature);
	if  lok&&rok {
		el1 := lSign.getElement()
		el2 := rSign.getElement()
		rs := pairing.NewG2().Mul(el1, el2)
		return BLSSignature(rs.Bytes())
	} else {
		return nil
	}
}

func (sig BLSSignature) Mul(other Signature) bool {
	if otherSign,ok := other.(BLSSignature); ok {
		el1 := sig.getElement()
		el2 := otherSign.getElement()
		rs := pairing.NewG2().Mul(el1, el2)
		copy(sig, rs.Bytes())
		return true
	} else {
		return false
	}
}

func (sig BLSSignature) Bytes() []byte {
	return sig
}

func (sig BLSSignature) IsZero() bool { return len(sig) == 0 }

func (sig BLSSignature) String() string { return fmt.Sprintf("/%X.../", Fingerprint(sig)) }

func (sig BLSSignature) Equals(other Signature) bool {
	if otherBLS, ok := other.(BLSSignature); ok {
		return sig.getElement().Equals(otherBLS.getElement())
	} else {
		return false
	}
}

func (p BLSSignature) MarshalJSON() ([]byte, error) {
	return data.Encoder.Marshal(p)
}

func (p *BLSSignature) UnmarshalJSON(enc []byte) error {
	var ref []byte
	err := data.Encoder.Unmarshal(&ref, enc)
	copy(*p, ref)
	return err
}

