package swap

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/big"
	"sort"
	"sync"

	"github.com/MinterTeam/minter-go-node/coreV2/types"
	"github.com/MinterTeam/minter-go-node/rlp"
	"github.com/cosmos/iavl"
)

const commissionOrder = 2

func (s *Swap) PairSellWithOrders(coin0, coin1 types.CoinID, amount0In, minAmount1Out *big.Int) (*big.Int, *big.Int, uint32, *ChangeDetailsWithOrders, []*OrderDetail) {
	pair := s.Pair(coin0, coin1)
	amount1Out, ownersMap, details := pair.SellWithOrders(amount0In)
	if amount1Out.Cmp(minAmount1Out) == -1 {
		panic(fmt.Sprintf("calculatedAmount1Out %s less minAmount1Out %s", amount1Out, minAmount1Out))
	}

	owners := sortOwners(ownersMap)
	for _, b := range owners {
		s.bus.Checker().AddCoin(coin0, big.NewInt(0).Neg(b.ValueBigInt))
	}
	s.bus.Checker().AddCoin(coin0, amount0In)
	s.bus.Checker().AddCoin(coin1, big.NewInt(0).Neg(amount1Out))
	return amount0In, amount1Out, pair.GetID(), details, owners
}

func (s *Swap) PairBuyWithOrders(coin0, coin1 types.CoinID, maxAmount0In, amount1Out *big.Int) (*big.Int, *big.Int, uint32, *ChangeDetailsWithOrders, []*OrderDetail) {
	pair := s.Pair(coin0, coin1)
	amount0In, ownersMap, details := pair.BuyWithOrders(amount1Out)
	if amount1Out.Cmp(maxAmount0In) == 1 {
		panic(fmt.Sprintf("calculatedAmount1Out %s less minAmount1Out %s", amount1Out, maxAmount0In))
	}

	owners := sortOwners(ownersMap)
	for _, b := range owners {
		s.bus.Checker().AddCoin(coin0, big.NewInt(0).Neg(b.ValueBigInt))
	}
	s.bus.Checker().AddCoin(coin0, amount0In)
	s.bus.Checker().AddCoin(coin1, big.NewInt(0).Neg(amount1Out))
	return amount0In, amount1Out, pair.GetID(), details, owners
}

type ChangeDetailsWithOrders struct {
	AmountIn            *big.Int
	AmountOut           *big.Int
	CommissionAmountIn  *big.Int
	CommissionAmountOut *big.Int
	Orders              []*Limit
}

func (c *ChangeDetailsWithOrders) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		AmountIn            string   `json:"amount_in"`
		AmountOut           string   `json:"amount_out"`
		CommissionAmountIn  string   `json:"commission_amount_in"`
		CommissionAmountOut string   `json:"commission_amount_out"`
		Orders              []*Limit `json:"orders"`
	}{
		AmountIn:            c.AmountIn.String(),
		AmountOut:           c.AmountOut.String(),
		CommissionAmountIn:  c.CommissionAmountIn.String(),
		CommissionAmountOut: c.CommissionAmountOut.String(),
		Orders:              c.Orders,
	})
}

type OrderDetail struct {
	Owner       types.Address `json:"seller"`
	ValueBigInt *big.Int      `json:"-"`
	Value       string        `json:"value"`
}

func (p *Pair) SellWithOrders(amount0In *big.Int) (amount1Out *big.Int, owners map[types.Address]*big.Int, c *ChangeDetailsWithOrders) {
	if amount0In == nil || amount0In.Sign() != 1 {
		panic(ErrorInsufficientInputAmount)
	}
	amount1Out, orders := p.calculateBuyForSellWithOrders(amount0In)
	if amount1Out == nil || amount1Out.Sign() != 1 {
		panic(ErrorInsufficientOutputAmount)
	}

	commission0orders, commission1orders, amount0, amount1, ownersMap := CalcDiffPool(amount0In, amount1Out, orders)

	//log.Println("uS", commission0orders, commission1orders)

	p.lockOrders.Lock()
	defer p.lockOrders.Unlock()

	if amount0.Sign() != 0 || amount1.Sign() != 0 {
		//log.Println("a", amount0, amount1)
		p.update(amount0, big.NewInt(0).Neg(amount1))
	}

	p.update(commission0orders, commission1orders)

	p.updateOrders(orders)

	p.orderSellByIndex(0) // update list

	return amount1Out, ownersMap, &ChangeDetailsWithOrders{
		AmountIn:            amount0,
		AmountOut:           amount1,
		CommissionAmountIn:  commission0orders,
		CommissionAmountOut: commission1orders,
		Orders:              orders,
	}
}

func sortOwners(owners map[types.Address]*big.Int) (result []*OrderDetail) {
	for address, b := range owners {
		result = append(result, &OrderDetail{
			Owner:       address,
			ValueBigInt: b,
			Value:       b.String(),
		})
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Owner.Compare(result[j].Owner) == -1
	})

	return result
}

func CalcDiffPool(amount0In, amount1Out *big.Int, orders []*Limit) (*big.Int, *big.Int, *big.Int, *big.Int, map[types.Address]*big.Int) {
	owners := map[types.Address]*big.Int{}

	amount0orders, amount1orders := big.NewInt(0), big.NewInt(0)
	commission0orders, commission1orders := big.NewInt(0), big.NewInt(0)
	for _, order := range orders {
		amount0orders.Add(amount0orders, order.WantBuy)
		amount1orders.Add(amount1orders, order.WantSell)

		cB := calcCommission999(order.WantBuy)
		cS := calcCommission999(order.WantSell)

		commission0orders.Add(commission0orders, cB)
		commission1orders.Add(commission1orders, cS)

		if owners[order.Owner] == nil {
			owners[order.Owner] = big.NewInt(0)
		}
		owners[order.Owner].Add(owners[order.Owner], big.NewInt(0).Sub(order.WantBuy, cB))
	}

	amount1orders.Sub(amount1orders, commission1orders)

	amount0 := big.NewInt(0).Sub(amount0In, amount0orders)
	amount1 := big.NewInt(0).Sub(amount1Out, amount1orders)

	return commission0orders, commission1orders, amount0, amount1, owners
}

func (p *Pair) BuyWithOrders(amount1Out *big.Int) (amount0In *big.Int, owners map[types.Address]*big.Int, c *ChangeDetailsWithOrders) { // todo: add mutex
	if amount1Out == nil || amount1Out.Sign() != 1 {
		panic(ErrorInsufficientInputAmount)
	}
	amount0In, orders := p.calculateSellForBuyWithOrders(amount1Out)
	if amount0In == nil || amount0In.Sign() != 1 {
		panic(ErrorInsufficientOutputAmount)
	}

	commission0orders, commission1orders, amount0, amount1, ownersMap := CalcDiffPool(amount0In, amount1Out, orders)

	//log.Println(commission0orders, commission1orders, "uB")

	p.lockOrders.Lock()
	defer p.lockOrders.Unlock()

	if amount0.Sign() != 0 || amount1.Sign() != 0 {
		//log.Println(amount0, amount1, "a")
		p.update(amount0, big.NewInt(0).Neg(amount1))
	}

	p.update(commission0orders, commission1orders)

	p.updateOrders(orders)

	p.orderSellByIndex(0) // update list

	return amount0In, ownersMap, &ChangeDetailsWithOrders{
		AmountIn:            amount0,
		AmountOut:           amount1,
		CommissionAmountIn:  commission0orders,
		CommissionAmountOut: commission1orders,
		Orders:              orders,
	}
}

func (p *Pair) updateOrders(orders []*Limit) {
	var editedOrders []*Limit
	for _, order := range orders {
		editedOrders = append(editedOrders, p.updateSellOrder(order.id, order.WantBuy, order.WantSell))
	}
}

func (p *Pair) updateSellOrder(id uint32, amount0, amount1 *big.Int) *Limit {
	limit := p.getOrder(id)
	newLimit := limit.sort()
	newLimit.OldSortPrice()

	limit.WantBuy.Sub(limit.WantBuy, amount0)
	limit.WantSell.Sub(limit.WantSell, amount1)

	p.MarkDirtyOrders(newLimit)

	return newLimit
}

func (p *Pair) resortSellOrderList(i int, limit *Limit) {
	if limit.isEmpty() {
		if !(limit.WantBuy.Sign() == 0 && limit.WantSell.Sign() == 0) {
			panic(fmt.Sprintf("zero value of %#v", limit))
		}
		p.unsetOrderSellByIndex(i)
		return
	}

	if limit.CmpOldRate() == 0 {
		return
	}

	log.Println("set new order position", p.isSorted(), // true
		limit.CmpOldRate(), limit.SortPrice().Text('f', 18), // 0.333333333333333315
		limit.OldSortPrice().Text('f', 18), // 0.333333333333333343
		p.Price().Text('f', 18),            // 0.333333333333333259
	)

	// cmp := p.DirectionSortPrice()

	// if !(limit.SortPrice().Cmp(p.SortPrice()) == cmp) {
	// 	p.unsetOrderSellByIndex(i)
	// 	return
	// }

	p.unsetOrderSellByIndex(i)

	// if limit.CmpOldRate() != cmp {
	// 	loadedLen := len(p.SellLowerOrders())
	// 	newIndex := p.setSellOrder(limit)
	// 	if newIndex == loadedLen {
	// 		p.unsetOrderSellByIndex(newIndex)
	//p.addOrder(limit)
	// }
	// }
}

func (l *Limit) isEmpty() (empty bool) {
	if l == nil {
		return true
	}
	if l.WantBuy.Sign() == 0 || l.WantSell.Sign() == 0 {
		empty = true
	}
	if !empty {
		return false
	}
	if l.WantBuy.Sign() != 0 || l.WantSell.Sign() != 0 {
		panic(fmt.Sprintf("order %d has one zero volume: %s, %s. Sell %v", l.id, l.WantBuy, l.WantSell, !l.IsBuy))
	}
	return true
}

func (l *Limit) isKeepRate() bool {
	if l == nil {
		return false
	}
	//fmt.Println("is keep", l.oldSortPrice, l.SortPrice())
	return l.CmpOldRate() == 0
}

func (l *Limit) CmpOldRate() int {
	return l.SortPrice().Cmp(l.OldSortPrice())
}

func (p *Pair) CalculateBuyForSellWithOrders(amount0In *big.Int) (amount1Out *big.Int) {
	amount1Out, _ = p.calculateBuyForSellWithOrders(amount0In)
	return amount1Out
}

func (p *Pair) calculateBuyForSellWithOrders(amount0In *big.Int) (amountOut *big.Int, orders []*Limit) {
	amountOut = big.NewInt(0)
	amountIn := big.NewInt(0).Set(amount0In)
	var pair EditableChecker = p
	for i := 0; true; i++ {
		if amountIn.Sign() == 0 {
			return amountOut, orders
		}

		limit := p.orderSellByIndex(i)
		if limit == nil {
			break
		}
		//log.Println("ow", limit.id, limit.Owner.String())

		price := limit.Price()
		if price.Cmp(pair.Price()) == -1 {
			reserve0diff, reserve1diff := pair.CalculateAddAmountsForPrice(price)
			if reserve0diff != nil && reserve1diff != nil {
				if amountIn.Cmp(reserve0diff) != 1 {
					break
				}

				amountIn.Sub(amountIn, reserve0diff)
				amountOut.Add(amountOut, reserve1diff)

				if err := pair.CheckSwap(reserve0diff, reserve1diff); err != nil {
					panic(err) // todo: for test
				}
				pair = pair.AddLastSwapStep(reserve0diff, reserve1diff)

				//log.Println("rS", reserve0diff, reserve1diff)
			}
		}

		// хотим продать 9009
		// проверяем есть ли столько на продажу
		//log.Println("amountIn", amountIn)
		rest := big.NewInt(0).Sub(amountIn, limit.WantBuy)
		if rest.Sign() != 1 {
			//log.Println("rest", rest)

			// 9009
			amount0 := big.NewInt(0).Set(amountIn)

			// считаем сколько сможем купить -- 3003
			amount1, acc := big.NewFloat(0).Mul(price, big.NewFloat(0).SetInt(amount0)).Int(nil)
			if acc != big.Exact {
				//log.Println("acc", acc)
			}

			//log.Println(limit.WantBuy, limit.WantSell)
			//log.Println(amount0, amount1)
			if amount1.Cmp(limit.WantSell) == 0 && amount0.Cmp(limit.WantBuy) != 0 {
				log.Println(limit.WantBuy, limit.WantSell, limit.id)
				log.Println(amount0, amount1)
				log.Panicln("neg BFS 0")
			}

			if amount1.Cmp(limit.WantSell) == 1 {
				amount1.Set(limit.WantSell)
				if amount0.Cmp(limit.WantBuy) == -1 {
					amount1.Sub(amount1, big.NewInt(1))
				}
			}

			//log.Println("amount1", amount1)

			orders = append(orders, &Limit{
				IsBuy:        limit.IsBuy,
				PairKey:      p.PairKey,
				WantBuy:      amount0, // 9009, 9 заберем в пул
				WantSell:     amount1, // 3003, 3 пул
				Owner:        limit.Owner,
				Height:       limit.Height,
				oldSortPrice: limit.SortPrice(),
				id:           limit.id,
				RWMutex:      new(sync.RWMutex),
			})

			comB := calcCommission999(amount1)
			//log.Println("comB", comB)
			amountOut.Add(amountOut, big.NewInt(0).Sub(amount1, comB))
			return amountOut, orders
		}

		orders = append(orders, &Limit{
			IsBuy:        limit.IsBuy,
			WantBuy:      big.NewInt(0).Set(limit.WantBuy),
			WantSell:     big.NewInt(0).Set(limit.WantSell),
			Owner:        limit.Owner,
			Height:       limit.Height,
			PairKey:      limit.PairKey,
			oldSortPrice: limit.SortPrice(),
			id:           limit.id,
			RWMutex:      limit.RWMutex,
		})

		comS := calcCommission999(limit.WantBuy)
		comB := calcCommission999(limit.WantSell)
		//log.Println("ccc1", comS, big.NewInt(0).Neg(comB))
		pair = pair.AddLastSwapStep(comS, big.NewInt(0).Neg(comB))
		amountOut.Add(amountOut, big.NewInt(0).Sub(limit.WantSell, comB))

		amountIn = rest
	}

	amount1diff := pair.CalculateBuyForSell(amountIn)
	if amount1diff != nil {
		if err := pair.CheckSwap(amountIn, amount1diff); err != nil {
			panic(err) // todo: for test
		}
		amountOut.Add(amountOut, amount1diff)
	}
	return amountOut, orders
}

func calcCommission001(amount0 *big.Int) *big.Int {
	mul := big.NewInt(0).Mul(amount0, big.NewInt(commissionOrder/2))
	quo := big.NewInt(0).Quo(mul, big.NewInt(1000))
	remainder := big.NewInt(0)
	if big.NewInt(0).Rem(mul, big.NewInt(1000)).Sign() == 1 {
		remainder = big.NewInt(1)
	}
	quo.Add(quo, remainder)
	return quo
}

func calcCommission999(amount1 *big.Int) *big.Int {
	quo := big.NewInt(0).Quo(amount1, big.NewInt(1000+commissionOrder/2))
	remainder := big.NewInt(0)
	if big.NewInt(0).Rem(amount1, big.NewInt(1000+commissionOrder/2)).Sign() == 1 {
		remainder = big.NewInt(1)
	}
	quo.Add(quo, remainder)
	return quo
}

func (p *Pair) CalculateAddAmountsForPrice(price *big.Float) (amount0In, amount1Out *big.Int) {
	if price.Cmp(p.Price()) == 1 {
		fmt.Println("price cur and first ord", price, p.Price())
		return nil, nil
	}
	return p.calculateAddAmountsForPrice(price)
}

// calculateAddAmountsForPrice returns a0 and a1 to reach the price
//		{ (r0 + 0.998a0) * (r1 - a1) = r0 * r1
//	   {
//		{ (r0 + a0) / (r1 - a1) = price
//
func (p *Pair) calculateAddAmountsForPrice(price *big.Float) (amount0 *big.Int, amount1 *big.Int) {
	reserve0, reserve1 := p.Reserves()
	r0 := big.NewFloat(0).SetInt(reserve0)
	r1 := big.NewFloat(0).SetInt(reserve1)
	k := big.NewFloat(0).Mul(r0, r1)
	r0Qrt := big.NewFloat(0).Mul(r0, r0)
	b := big.NewFloat(0).Mul(big.NewFloat((2000.0-commission)/2), r0)
	kMulPrice := big.NewFloat(0).Mul(k, big.NewFloat(0).Quo(big.NewFloat(1), price))
	r0QrtSubKMulPrice := big.NewFloat(0).Sub(r0Qrt, kMulPrice)
	d := big.NewFloat(0).Sub(big.NewFloat(0).Mul(big.NewFloat((2000-commission)*(2000-commission)/4), r0Qrt), big.NewFloat(0).Mul(big.NewFloat(2000*(1000-commission)/2), r0QrtSubKMulPrice))
	x1 := big.NewFloat(0).Quo(big.NewFloat(0).Add(big.NewFloat(0).Neg(b), big.NewFloat(0).Sqrt(d)), big.NewFloat(1000-commission))
	amount0, _ = x1.Int(nil)

	if amount0 == nil || amount0.Sign() != 1 {
		return nil, nil
	}

	return amount0, p.CalculateBuyForSell(amount0)
}

func (p *Pair) CalculateSellForBuyWithOrders(amount1Out *big.Int) (amount0In *big.Int) {
	amount0In, _ = p.calculateSellForBuyWithOrders(amount1Out)
	return amount0In
}

func (p *Pair) calculateSellForBuyWithOrders(amount1Out *big.Int) (amountIn *big.Int, orders []*Limit) {
	amountIn = big.NewInt(0)
	amountOut := big.NewInt(0).Set(amount1Out)
	var pair EditableChecker = p
	for i := 0; true; i++ {
		if amountOut.Sign() == 0 {
			return amountIn, orders
		}

		limit := p.orderSellByIndex(i)
		if limit == nil {
			break
		}
		//log.Println("ow", limit.id, limit.Owner.String())

		price := limit.Price()
		if price.Cmp(pair.Price()) == -1 {
			reserve0diff, reserve1diff := pair.CalculateAddAmountsForPrice(price)
			//log.Println(reserve1diff, "ooo")
			if reserve1diff != nil && reserve0diff != nil {
				if amountOut.Cmp(reserve1diff) != 1 {
					break
				}
				amountOut.Sub(amountOut, reserve1diff)
				amountIn.Add(amountIn, reserve0diff)

				if err := pair.CheckSwap(reserve0diff, reserve1diff); err != nil {
					panic(err) // todo: for test
				}
				pair = pair.AddLastSwapStep(reserve0diff, reserve1diff)

				//log.Println("rB", reserve0diff, reserve1diff)
			}
		}

		// хочу купить amountOut = 3000
		// тк мы 0.1% положим в пул, то надо купить 3003
		// проверим что в пуле есть X * 0.999 == 3000
		// на продажу есть 5000
		// что бы в пул пошел 0.1%, мне надо купить 3003 из которых 3 положить в пул
		comB := calcCommission999(limit.WantSell)
		//log.Println("amountOut", amountOut)
		//log.Println("comB", comB)
		rest := big.NewInt(0).Sub(amountOut, big.NewInt(0).Sub(limit.WantSell, comB))
		if rest.Sign() != 1 {

			amount1 := big.NewInt(0).Add(amountOut, calcCommission001(amountOut))
			//log.Println("amount1", amount1)
			// считаем сколько монет надо продать что бы купить 3003
			amount0, acc := big.NewFloat(0).Quo(big.NewFloat(0).SetInt(amount1), price).Int(nil)
			if acc != big.Exact {
				//log.Println("acc", acc) // todo
			}
			//log.Println("amount0", amount0)

			if amount1.Cmp(limit.WantSell) == 0 && amount0.Cmp(limit.WantBuy) != 0 {
				log.Println(limit.WantBuy, limit.WantSell, limit.id)
				log.Println(amount0, amount1)
				log.Panicln("neg SFB 0")

				//if amount0.Cmp(limit.WantBuy) != -1 {
				//	amount0.Set(limit.WantBuy)
				//	amount1.Set(limit.WantSell)
				//}
			}
			if amount1.Cmp(limit.WantSell) != 0 && amount0.Cmp(limit.WantBuy) == 0 {
				amount0.Set(limit.WantBuy)
				amount1.Set(limit.WantSell)
			}

			orders = append(orders, &Limit{
				IsBuy:        limit.IsBuy,
				PairKey:      p.PairKey,
				WantBuy:      big.NewInt(0).Set(amount0), // и того продам по ордеру 9009, из них 9000 продавцу и 9 в пул
				WantSell:     amount1,                    // 3003, позже вычтем 3 и положим в пул
				Owner:        limit.Owner,
				Height:       limit.Height,
				oldSortPrice: limit.SortPrice(),
				id:           limit.id,
				RWMutex:      new(sync.RWMutex),
			})

			amountIn.Add(amountIn, amount0)
			return amountIn, orders
		}

		//log.Println("order more")
		orders = append(orders, &Limit{
			IsBuy:        limit.IsBuy,
			WantBuy:      big.NewInt(0).Set(limit.WantBuy),
			WantSell:     big.NewInt(0).Set(limit.WantSell),
			Owner:        limit.Owner,
			Height:       limit.Height,
			PairKey:      limit.PairKey,
			oldSortPrice: limit.SortPrice(),
			id:           limit.id,
			RWMutex:      limit.RWMutex,
		})

		comS := calcCommission999(limit.WantBuy)

		pair = pair.AddLastSwapStep(comS, big.NewInt(0).Neg(comB))
		amountOut = rest

		amountIn.Add(amountIn, limit.WantBuy)
		// amountIn.Add(amountIn, comS)
	}

	amount0diff := pair.CalculateSellForBuy(amountOut)
	if amount0diff != nil {
		if err := pair.CheckSwap(amount0diff, amountOut); err != nil {
			panic(err) // todo: for test
		}
		amountIn.Add(amountIn, amount0diff)
	}
	return amountIn, orders
}

func CalcPriceSell(sell, buy *big.Int) *big.Float {
	return new(big.Float).SetPrec(precision).Quo(
		big.NewFloat(0).SetInt(buy),
		big.NewFloat(0).SetInt(sell),
	)
}

type Limit struct {
	WantBuy  *big.Int
	WantSell *big.Int
	Owner    types.Address
	IsBuy    bool
	Height   uint64

	PairKey
	oldSortPrice *big.Float
	id           uint32

	*sync.RWMutex
}

func (l *Limit) ID() uint32 {
	if l == nil {
		return 0 // todo: test
	}

	return l.id
}

func (l *Limit) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		WantBuy  string `json:"buy"`
		WantSell string `json:"sell"`
		Owner    string `json:"seller"`
		ID       uint32 `json:"id"`
	}{
		WantBuy:  l.WantBuy.String(),
		WantSell: l.WantSell.String(),
		Owner:    l.Owner.String(),
		ID:       l.id,
	})
}

type limits struct {
	ids []uint32
}

type orderList struct {
	mu   sync.RWMutex
	list map[uint32]*Limit // list sorted dirties Order
}

type orderDirties struct {
	mu   sync.RWMutex
	list map[uint32]struct{}
}

const (
	precision = 54 // supported precision
)

func (l *Limit) Price() *big.Float {
	if l.isEmpty() {
		return big.NewFloat(0)
	}
	l.RLock()
	defer l.RUnlock()
	return CalcPriceSell(l.WantBuy, l.WantSell)
}

func (p *Pair) Price() *big.Float {
	return p.pairData.Price()
}

func (p *Pair) getOrder(id uint32) *Limit {
	p.orders.mu.Lock() // todo: test edition
	defer p.orders.mu.Unlock()

	return p.order(id)
}

func (p *Pair) order(id uint32) *Limit {
	l, ok := p.orders.list[id]
	if ok {
		if p.isSorted() {
			return l
		}

		return l.Reverse()
	}

	l = p.loadOrder(id)
	if l == nil {
		return nil
	}

	p.orders.list[id] = l

	if p.isSorted() {
		return l
	}

	return l.Reverse()
}

func (p *Pair) GetOrders(ids []uint32) []*Limit {
	p.lockOrders.Lock()
	defer p.lockOrders.Unlock()

	return p.getOrders(ids)
}
func (p *Pair) getOrders(ids []uint32) []*Limit {
	p.orders.mu.Lock() // todo: test edition
	defer p.orders.mu.Unlock()

	var result []*Limit
	for _, id := range ids {
		result = append(result, p.order(id))
	}
	return result
}

func (p *Pair) setOrder(l *Limit) {
	p.orders.mu.Lock()
	defer p.orders.mu.Unlock()
	p.orders.list[l.id] = l
}

func (p *Pair) SortPrice() *big.Float {
	if p.isSorted() {
		return p.pairData.Price()
	}
	return p.pairData.reverse().Price()
}

func (p *Pair) DirectionSortPrice() int {
	if !p.isSorted() {
		return 1
	}
	return -1
}

func (l *Limit) SortPrice() *big.Float {
	if l.isSorted() {
		return l.Price()
	}
	return l.Reverse().Price()
}

func (l *Limit) OldSortPrice() *big.Float {
	if l.oldSortPrice == nil {
		l.oldSortPrice = new(big.Float).SetPrec(precision).Set(l.SortPrice())
	}

	return new(big.Float).SetPrec(precision).Set(l.oldSortPrice)
}

func (l *Limit) isSell() bool {
	return !l.IsBuy
}

// ReCalcOldSortPrice saves before change, need for update on disk
func (l *Limit) ReCalcOldSortPrice() *big.Float {
	l.oldSortPrice.Set(l.SortPrice())
	return l.OldSortPrice()
}

func (l *Limit) Reverse() *Limit {
	l.RLock()
	defer l.RUnlock()
	return &Limit{
		PairKey:      l.PairKey.reverse(),
		IsBuy:        !l.IsBuy,
		WantBuy:      l.WantSell,
		WantSell:     l.WantBuy,
		Owner:        l.Owner,
		Height:       l.Height,
		oldSortPrice: l.oldSortPrice,
		id:           l.id,
		RWMutex:      l.RWMutex,
	}
}

func (l *Limit) sort() *Limit {
	if l.isSorted() {
		return l
	}

	return l.Reverse()
}

func (l *Limit) isSorted() bool {
	return l.PairKey.isSorted()
}

func (l *Limit) clone() *Limit {
	if l == nil {
		return nil
	}
	return &Limit{
		PairKey:      l.PairKey,
		IsBuy:        l.IsBuy,
		WantBuy:      big.NewInt(0).Set(l.WantBuy),
		WantSell:     big.NewInt(0).Set(l.WantSell),
		Owner:        l.Owner,
		Height:       l.Height,
		oldSortPrice: big.NewFloat(0).Set(l.oldSortPrice),
		id:           l.id,
		RWMutex:      &sync.RWMutex{},
	}
}

func (p *Pair) MarkDirtyOrders(order *Limit) {
	p.markDirtyOrders()

	if order.isEmpty() {
		p.setDeletedSellOrderIDs(order.id)
	} else if !order.isKeepRate() {
		p.setUnsortedSellOrder(order.id)
	}

	p.dirtyOrders.mu.Lock()
	p.dirtyOrders.list[order.id] = struct{}{}
	p.dirtyOrders.mu.Unlock()
	return
}

func (p *Pair) setSellOrder(new *Limit) (index int) {
	cmp := p.DirectionSortPrice()

	//p.setOrder(new)
	p.orderSellByIndex(0)
	orders := p.SellOrderIDs()
	for i, limitID := range orders {
		limit := p.getOrder(limitID)
		if !(new.SortPrice().Cmp(limit.SortPrice()) != cmp) {
			index = i + 1
			continue
		}
		break
	}

	if index == 0 {
		p.setSellOrders(append([]uint32{new.id}, orders...))
		return
	}

	if index == len(orders) {
		p.setSellOrders(append(orders, new.id))
		return
	}

	p.setSellOrders(append(orders[:index], append([]uint32{new.id}, orders[index:]...)...))
	return
}

func (p *Pair) SellOrderIDs() []uint32 {
	if p.isSorted() {
		return p.sellOrders.ids
	}
	return p.buyOrders.ids
}

func (p *Pair) BuyOrderIDs() []uint32 {
	if p.isSorted() {
		return p.buyOrders.ids
	}
	return p.sellOrders.ids
}

func (p *Pair) isUnsortedSellOrder(id uint32) bool {
	ds := p.unsortedSellOrderIDs()
	ds.mu.RLock()
	defer ds.mu.RUnlock()

	_, ok := ds.list[id]
	return ok
}
func (p *Pair) isUnsortedBuyOrder(id uint32) bool {
	ds := p.unsortedBuyOrderIDs()
	ds.mu.RLock()
	defer ds.mu.RUnlock()

	_, ok := ds.list[id]
	return ok
}
func (p *Pair) hasUnsortedSellOrders() bool {
	ds := p.unsortedSellOrderIDs()
	ds.mu.RLock()
	defer ds.mu.RUnlock()

	return len(ds.list) > 0
}

func (p *Pair) unsortedSellOrderIDs() *orderDirties {
	if p.isSorted() {
		return p.unsortedDirtySellOrders
	}
	return p.unsortedDirtyBuyOrders
}
func (p *Pair) setUnsortedSellOrder(id uint32) {
	ds := p.unsortedSellOrderIDs()
	ds.mu.Lock()
	defer ds.mu.Unlock()
	ds.list[id] = struct{}{}
}
func (p *Pair) unsortedBuyOrderIDs() *orderDirties {
	if p.isSorted() {
		return p.unsortedDirtyBuyOrders
	}
	return p.unsortedDirtySellOrders
}
func (p *Pair) hasDeletedSellOrders() bool {
	ds := p.deletedSellOrderIDs()
	ds.mu.RLock()
	defer ds.mu.RUnlock()
	return len(ds.list) > 0
}
func (p *Pair) setDeletedSellOrderIDs(id uint32) {
	ds := p.deletedSellOrderIDs()
	ds.mu.Lock()
	defer ds.mu.Unlock()
	ds.list[id] = struct{}{}
}
func (p *Pair) isDeletedSellOrder(id uint32) bool {
	ds := p.deletedSellOrderIDs()
	ds.mu.RLock()
	defer ds.mu.RUnlock()
	_, ok := ds.list[id]
	return ok
}
func (p *Pair) isDeletedBuyOrder(id uint32) bool {
	ds := p.deletedBuyOrderIDs()
	ds.mu.RLock()
	defer ds.mu.RUnlock()
	_, ok := ds.list[id]
	return ok
}
func (p *Pair) deletedSellOrderIDs() *orderDirties {
	if p.isSorted() {
		return p.deletedSellOrders
	}
	return p.deletedBuyOrders
}
func (p *Pair) deletedBuyOrderIDs() *orderDirties {
	if p.isSorted() {
		return p.deletedBuyOrders
	}
	return p.deletedSellOrders
}

func (p *Pair) loadedSellOrderIDs() []uint32 {
	return p.loadedSellOrders.ids
}

func (p *Pair) loadedBuyOrderIDs() []uint32 {
	return p.loadedBuyOrders.ids
}

func (p *Pair) setSellOrders(orders []uint32) {
	if p.isSorted() {
		p.sellOrders.ids = orders
		return
	}
	p.buyOrders.ids = orders
	return
}
func (p *Pair) setLoadedSellOrders(orders []uint32) {
	p.loadedSellOrders.ids = orders
	return
}
func (p *Pair) setLoadedBuyOrders(orders []uint32) {
	p.loadedBuyOrders.ids = orders
	return
}

func (p *Pair) setBuyOrders(orders []uint32) {
	if p.isSorted() {
		p.buyOrders.ids = orders
		return
	}
	p.sellOrders.ids = orders
	return
}

func (s *Swap) PairAddOrder(coinWantBuy, coinWantSell types.CoinID, wantBuyAmount, wantSellAmount *big.Int, sender types.Address, block uint64) (uint32, uint32) {
	pair := s.Pair(coinWantBuy, coinWantSell)
	order := pair.AddOrder(wantBuyAmount, wantSellAmount, sender, block)

	s.bus.Checker().AddCoin(coinWantSell, wantSellAmount)

	return order.id, pair.GetID()
}

func (s *Swap) PairRemoveLimitOrder(id uint32) (types.CoinID, *big.Int) {
	order := s.GetOrder(id)
	if order == nil {
		panic("order not exist")
	}

	if !order.isSell() {
		order = order.Reverse()
	}

	pair := s.Pair(order.Coin0, order.Coin1)
	if pair.IsOrderAlreadyUsed(id) {
		panic("order already used")
	}

	returnVolume := big.NewInt(0).Set(order.WantSell)

	s.bus.Checker().AddCoin(order.Coin1, big.NewInt(0).Neg(returnVolume))

	pair.lockOrders.Lock()
	defer pair.lockOrders.Unlock()

	pair.updateOrders([]*Limit{order})
	pair.orderSellByIndex(0) // update list
	return order.Coin1, returnVolume
}

func (s *Swap) PairAddOrderWithID(coinWantBuy, coinWantSell types.CoinID, wantBuyAmount, wantSellAmount *big.Int, sender types.Address, id uint32, height uint64) (uint32, uint32) {
	pair := s.Pair(coinWantBuy, coinWantSell)
	order := pair.AddOrderWithID(wantBuyAmount, wantSellAmount, sender, id, height)

	s.bus.Checker().AddCoin(coinWantSell, wantSellAmount)

	return order.id, pair.GetID()
}

func (p *Pair) GetOrder(id uint32) *Limit {
	return p.getOrder(id)
}
func (p *Pair) IsOrderAlreadyUsed(id uint32) bool {
	if p.isDeletedBuyOrder(id) || p.isDeletedSellOrder(id) {
		return true
	}

	return p.getOrder(id) == nil
}

func (p *Pair) AddOrder(wantBuyAmount0, wantSellAmount1 *big.Int, sender types.Address, block uint64) (order *Limit) {
	order = &Limit{
		PairKey:      p.PairKey,
		IsBuy:        false,
		WantBuy:      wantBuyAmount0,
		WantSell:     wantSellAmount1,
		id:           p.getLastTotalOrderID(),
		oldSortPrice: new(big.Float).SetPrec(precision),
		Owner:        sender,
		RWMutex:      new(sync.RWMutex),
		Height:       block,
	}
	sortedOrder := order.sort()

	p.lockOrders.Lock()
	defer p.lockOrders.Unlock()

	p.MarkDirtyOrders(sortedOrder)

	p.setOrder(sortedOrder)
	p.orderSellByIndex(0)
	return order
}

func (p *Pair) AddOrderWithID(wantBuyAmount0, wantSellAmount1 *big.Int, sender types.Address, id uint32, height uint64) (order *Limit) {
	order = &Limit{
		PairKey:      p.PairKey,
		IsBuy:        false,
		WantBuy:      wantBuyAmount0,
		WantSell:     wantSellAmount1,
		id:           id,
		oldSortPrice: new(big.Float).SetPrec(precision),
		Owner:        sender,
		Height:       height,
		RWMutex:      new(sync.RWMutex),
	}
	sortedOrder := order.sort()

	p.lockOrders.Lock() // todo: tests
	defer p.lockOrders.Unlock()

	p.MarkDirtyOrders(sortedOrder)

	p.setOrder(sortedOrder)
	p.orderSellByIndex(0)
	return order
}

func (p *Pair) addOrder(limit *Limit) {
	if p.Price().Cmp(limit.Price()) == -1 {
		log.Println("Higher")
	}

	firstSavedOrder := p.orderSellByIndex(0)
	if firstSavedOrder != nil {
		return
	}

	// todo: remove
	orders, count := p.updateDirtyOrders([]uint32{}, true)
	log.Panicln("addOrder: del", count)
	p.setSellOrders(orders)
	return
}

func (p *Pair) loadAllOrders(immutableTree *iavl.ImmutableTree) (orders []*Limit) {
	const countFirstBytes = 10

	startKey := append(append([]byte{mainPrefix}, p.pathOrders()...), byte(0), byte(0))
	endKey := append(append(append([]byte{mainPrefix}, p.pathOrders()...), byte(1), byte(255)), id2Bytes(math.MaxUint32)...)

	immutableTree.IterateRange(startKey, endKey, true, func(key []byte, value []byte) bool {
		var isSell = true
		if key[countFirstBytes : countFirstBytes+1][0] == 0 {
			isSell = false
		}

		id := binary.BigEndian.Uint32(key[len(key)-4:])
		if isSell {
			id = math.MaxUint32 - id
		}
		order := p.getOrder(id)
		orders = append(orders, order)

		return false
	})

	return orders
}

func (s *Swap) loadBuyOrders(pair *Pair, fromOrder *Limit, limit int) []uint32 {
	endKey := append(append(append([]byte{mainPrefix}, pair.pathOrders()...), byte(0), byte(255)), id2Bytes(math.MaxUint32)...)
	var startKey = append(append([]byte{mainPrefix}, pair.pathOrders()...), byte(0), byte(0))

	var loadedAll bool
	ids := pair.loadedBuyOrderIDs()
	if len(ids) != 0 && ids[len(ids)-1] == 0 {
		loadedAll = true
		ids = ids[:len(ids)-1]
	}

	if fromOrder == nil && len(ids) >= limit {
		return ids[:limit]
	}

	k := 1
	var slice []uint32
	for i, id := range ids {
		if id == fromOrder.ID() {
			if len(ids[i+1:]) < limit {
				slice = append(slice, ids[i+1:]...)
				k += len(ids[i+1:])
				fromOrder = pair.getOrder(ids[len(ids)-1])
				break
			}

			return ids[i+1 : i+limit+1]
		}
	}

	if loadedAll {
		return append(slice, 0)
	}

	if fromOrder != nil { //  todo .isKeepRate()
		startKey = pricePath(pair.PairKey, fromOrder.OldSortPrice(), fromOrder.id+1, false) // todo: tests OldSortPrice
	}

	var has bool
	s.immutableTree().IterateRange(startKey, endKey, true, func(key []byte, _ []byte) bool {
		has = true
		if k > limit {
			return true
		}

		id := binary.BigEndian.Uint32(key[len(key)-4:])

		slice = append(slice, id)
		k++
		return false
	})

	if !has {
		slice = append(slice, 0)
	}

	pair.setLoadedBuyOrders(append(ids, slice...))
	return slice
}

func (s *Swap) GetOrder(id uint32) *Limit {
	order := s.loadOrder(id)
	if order == nil {
		return nil
	}

	pair := s.Pair(order.Coin0, order.Coin1)

	if pair.isDeletedBuyOrder(id) || pair.isDeletedSellOrder(id) {
		return nil
	}

	list := pair.orders
	list.mu.Lock()
	defer list.mu.Unlock()

	if o, ok := list.list[id]; ok {
		return o
	}

	list.list[id] = order
	return order
}

func (s *Swap) loadOrder(id uint32) *Limit {
	_, value := s.immutableTree().Get(pathOrder(id))
	if value == nil {
		return nil
	}

	order := &Limit{
		id:           id,
		oldSortPrice: new(big.Float).SetPrec(precision),
		RWMutex:      new(sync.RWMutex),
	}
	err := rlp.DecodeBytes(value, order)
	if err != nil {
		panic(err)
	}

	order.ReCalcOldSortPrice()

	return order
}

func (s *Swap) loadSellOrders(pair *Pair, fromOrder *Limit, limit int) []uint32 {
	startKey := append(append([]byte{mainPrefix}, pair.pathOrders()...), byte(1), byte(0))
	//var endKey = append(append([]byte{mainPrefix}, pair.pathOrders()...), byte(1), byte(255))
	var endKey = append(append(append([]byte{mainPrefix}, pair.pathOrders()...), byte(1), byte(255)), id2Bytes(math.MaxUint32)...)

	var loadedAll bool
	ids := pair.loadedSellOrderIDs()
	if len(ids) != 0 && ids[len(ids)-1] == 0 {
		loadedAll = true
		ids = ids[:len(ids)-1]
	}

	if fromOrder == nil && len(ids) >= limit {
		return ids[:limit]
	}

	k := 1
	var slice []uint32
	for i, id := range ids {
		if id == fromOrder.ID() {
			if len(ids[i+1:]) < limit {
				slice = append(slice, ids[i+1:]...)
				k += len(ids[i+1:])
				fromOrder = pair.getOrder(ids[len(ids)-1])
				break
			}

			return ids[i+1 : i+limit+1]
		}
	}

	if loadedAll {
		return append(slice, 0)
	}

	if fromOrder != nil { //  todo .isKeepRate()
		endKey = pricePath(pair.PairKey, fromOrder.OldSortPrice(), fromOrder.id, true) // todo: tests OldSortPrice
	}

	var has bool
	s.immutableTree().IterateRange(startKey, endKey, false, func(key []byte, value []byte) bool {
		has = true
		if k > limit {
			return true
		}

		id := math.MaxUint32 - binary.BigEndian.Uint32(key[len(key)-4:])

		slice = append(slice, id)
		k++
		return false
	})

	if !has || len(slice) < limit {
		slice = append(slice, 0)
	}

	pair.setLoadedSellOrders(append(ids, slice...))
	return slice
}

func (p *Pair) updateDirtyOrders(list []uint32, lower bool) (orders []uint32, delCount int) {
	unsortedDirtySellOrders := p.unsortedSellOrderIDs()
	unsortedDirtySellOrders.mu.Lock()
	defer unsortedDirtySellOrders.mu.Unlock()

	deletedDirtySellOrders := p.deletedSellOrderIDs()
	deletedDirtySellOrders.mu.Lock()
	defer deletedDirtySellOrders.mu.Unlock()

	var limits []*Limit
	for _, orderID := range list {
		if _, ok := deletedDirtySellOrders.list[orderID]; ok {
			delCount++
			continue
		}
		if _, ok := unsortedDirtySellOrders.list[orderID]; ok {
			delCount++
			continue
		}

		order := p.getOrder(orderID)
		limits = append(limits, order)
		if order == nil {
			break
		}
	}

	cmp := p.DirectionSortPrice()
	if !lower {
		cmp *= -1
	}

	var dirties []*Limit
	for orderID := range unsortedDirtySellOrders.list {
		dirty := p.getOrder(orderID)
		dirties = append(dirties, dirty)
	}

	sort.Slice(dirties, func(i, j int) bool {
		a := dirties[j]
		b := dirties[i]
		switch a.SortPrice().Cmp(b.SortPrice()) {
		case cmp:
			return true
		case 0:
			return a.id < b.id
		default:
			return false
		}
	})

	var pos int
	for _, dirty := range dirties {
		var isSet bool
		limits, isSet, pos = addToList(limits, dirty, cmp, pos)
		if isSet {
			delCount--
			delete(unsortedDirtySellOrders.list, dirty.id)
		} else {
			break
		}
	}

	for _, order := range limits {
		if order == nil {
			return append(orders, 0), delCount
		}
		orders = append(orders, order.id)
	}

	return orders, delCount
}

func addToList(orders []*Limit, dirtyOrder *Limit, cmp int, index int) (list []*Limit, included bool, pos int) {

	var hasZero bool
	if true {

		var last int
		if len(orders) != 0 && orders[len(orders)-1] == nil {
			hasZero = true
			last = 1
		}

		skeeped := index
		var start, end = index, len(orders) - last
		var slice = orders[start:end]
		for len(slice) > 0 {
			cur := len(slice) / 2
			limit := slice[cur]
			if limit.id == dirtyOrder.id {
				log.Panicln("dirty ID == in list ID", limit.id)
			}

			//log.Println("start", skeeped, "stop", skeeped+len(slice)-1, "cur", cur, "id", dirtyOrder.id)

			less := false
			switch dirtyOrder.SortPrice().Cmp(limit.SortPrice()) {
			case cmp:
				less = true
			case 0:
				if dirtyOrder.id > limit.id {
					less = true
				} else {
					less = false
				}
			default:
				less = false
			}

			if less {
				skeeped += cur + 1
				index = 0
				slice = slice[cur+1:]
			} else {
				index = cur
				slice = slice[:cur]
			}
			//log.Println("slice", slice)
		}
		index += skeeped
	} else {
		for i, limit := range orders {
			if limit == nil {
				hasZero = true
				index = i
				break
			}
			if limit.id == dirtyOrder.id {
				log.Println("equal ID in addToList")
				return orders, true, i
			}

			var ok bool
			switch dirtyOrder.SortPrice().Cmp(limit.SortPrice()) {
			case cmp:
				index = i + 1
				continue
			case 0:
				index = i
				if dirtyOrder.id > limit.id {
					index = i + 1
				}
				//log.Println("sort of equal orders", dirtyOrder.id, limit.id, orders, index)
			default:
				//log.Println("sort order result", dirtyOrder.id, orders, index)
				ok = true
			}
			if ok {
				break
			}
		}
	}

	//log.Println("index", index)

	if index == 0 {
		return append([]*Limit{dirtyOrder}, orders...), true, 0
	}

	if index == len(orders) {
		if hasZero {
			return append(orders[:len(orders)-1], dirtyOrder, nil), true, index
		}
		return orders, false, -1
	}

	return append(orders[:index], append([]*Limit{dirtyOrder}, orders[index:]...)...), true, index
}

func (p *Pair) OrderIDBuyByIndex(index int) uint32 {
	p.lockOrders.Lock()
	defer p.lockOrders.Unlock()

	return p.orderIDBuyByIndex(index)
}

func (p *Pair) orderIDBuyByIndex(index int) uint32 {
	orders := p.BuyOrderIDs()
	var count int
	var deleteCount int
	for firstIterate := true; (firstIterate && len(orders) <= index) || deleteCount != 0; firstIterate = false {
		//orders, deleteCount = p.updateDirtyOrders(nil, p.loadBuyOrders(p, orders, index+count), false)
		count += deleteCount
	}

	p.setBuyOrders(orders)

	if len(orders)-1 < index {
		return 0
	}

	return orders[index]
}

// Deprecated
func (p *Pair) OrderBuyByIndex(index int) *Limit {
	p.lockOrders.Lock()
	defer p.lockOrders.Unlock()

	return p.orderBuyByIndex(index)
}

// Deprecated
func (p *Pair) orderBuyByIndex(index int) *Limit {
	orders := p.BuyOrderIDs()

	var fromOrder *Limit
	if len(orders) != 0 && orders[0] != 0 {
		fromOrder = p.getOrder(orders[0])
		if p.isUnsortedBuyOrder(fromOrder.id) {
			loadedOrders := p.loadBuyOrders(p, fromOrder, index+1)
			var resortedOrders []uint32
			var unsets int
			if (len(loadedOrders) == 0 || loadedOrders[len(loadedOrders)-1] == 0) && len(orders) != 0 {
				resortedOrders, unsets = p.updateDirtyOrders(orders, false)
			} else {
				resortedOrders, unsets = p.updateDirtyOrders(loadedOrders, false)
			}
			for unsets > 0 && len(resortedOrders) != 0 && resortedOrders[len(resortedOrders)-1] != 0 {
				id := resortedOrders[len(resortedOrders)-1]
				fromOrder = p.getOrder(id)
				loadedNextOrders := p.loadBuyOrders(p, fromOrder, index+1)
				resortedOrders, unsets = p.updateDirtyOrders(append(orders, loadedNextOrders...), false)
			}
			orders = resortedOrders
		} else {
			if index > len(orders)-1 && len(orders) != 0 && orders[len(orders)-1] != 0 {
				fromOrder = p.getOrder(orders[len(orders)-1])
				loadedNextOrders := p.loadBuyOrders(p, fromOrder, index+1)
				orders, _ = p.updateDirtyOrders(append(orders, loadedNextOrders...), false)
			} else {
				orders, _ = p.updateDirtyOrders(orders, false)
			}
		}
	} else {
		orders = p.loadBuyOrders(p, nil, index+1)
		orders, _ = p.updateDirtyOrders(orders, false)
	}

	p.setBuyOrders(orders)

	if len(orders)-1 < index {
		return nil
	}

	order := p.getOrder(orders[index])

	return order
}

// Deprecated
func (p *Pair) OrderBuyLast() (limit *Limit, index int) {
	p.lockOrders.Lock()
	defer p.lockOrders.Unlock()
	return p.orderBuyLast()
}

// Deprecated
func (p *Pair) orderBuyLast() (limit *Limit, index int) {
	for order := p.orderBuyByIndex(index); order != nil; order = p.orderBuyByIndex(index) {
		limit = order
		index++
	}
	return limit, index - 1
}

func (p *Pair) unsetOrderSellByIndex(index int) {
	slice := p.SellOrderIDs()
	length := len(slice)

	if length <= index {
		panic(fmt.Sprintf("slice len %d, want index %d", length, index))
	}

	if length == 1 {
		p.setSellOrders(nil)
		return
	}

	switch index {
	case 0:
		slice = slice[index+1:]
	case length - 1:
		slice = slice[:index]
	default:
		slice = append(slice[:index], slice[index+1:]...)
	}

	p.setSellOrders(slice)
	return
}

func (p *Pair) OrderSellByIndex(index int) *Limit {
	p.lockOrders.Lock()
	defer p.lockOrders.Unlock()
	return p.orderSellByIndex(index)
}

func (p *Pair) orderSellByIndex(index int) *Limit {
	orders := p.SellOrderIDs()
	//log.Println("orders start", orders)
	var fromOrder *Limit
	// если массив не пустой, то пересортировать, если есть грязные!
	if len(orders) != 0 {
		// если есть грязные.
		if p.hasUnsortedSellOrders() || p.hasDeletedSellOrders() {
			// пересортируем, что бы лист почистился и пересортировался
			orders, _ = p.updateDirtyOrders(orders, true)

			lastI := len(orders) - 1

			// если загружены не все
			if lastI >= 0 && orders[lastI] != 0 {
				// проверяем есть ли среди этого массива, элемент с нужным индексом
				if index > lastI {
					// загрузим с последнего нужное количество и отсортируем
					fromOrder = p.getOrder(orders[lastI])
					loadedNextOrders := p.loadSellOrders(p, fromOrder, index-lastI) // todo var
					resortedOrders, unsets := p.updateDirtyOrders(append(orders, loadedNextOrders...), true)
					//resortedOrders, unsets := p.updateDirtyOrders(append(orders, loadedNextOrders...), true)
					// проверим загружены ли все
					lastJ := len(resortedOrders) - 1
					if resortedOrders[lastJ] != 0 {
						// среди них не может быть использованных иначе бы они были загружены ранее,
						// но могут быть удаленные удаленных, проверим
						for ; index > lastJ && lastJ >= 0 && resortedOrders[lastJ] != 0 && p.hasDeletedSellOrders() && unsets > 0; lastJ = len(resortedOrders) - 1 {
							fromOrder = p.getOrder(resortedOrders[lastI])
							loadedNextOrders := p.loadSellOrders(p, fromOrder, index-lastI+unsets)
							var resortLoadedNextOrders []uint32
							resortLoadedNextOrders, unsets = p.updateDirtyOrders(loadedNextOrders, true)
							//resortedOrders, unsets := p.updateDirtyOrders(append(orders, loadedNextOrders...), true)
							resortedOrders = append(resortedOrders, resortLoadedNextOrders...)
						}
					}
					orders = resortedOrders

					//else {
					// загружено все что есть
					// тут нужно выйти и отдать что есть
					// todo
					//}
					// тут уже был выход
				} else {
					// тут нужно выйти и отдать элемент
					// todo
				}

			} else {
				// если загружены все
				// выйти и отдать
			}
		} else {
			// проверим количество
			lastI := len(orders) - 1
			// если загружены не все и их не достаточно, то подгрузить
			if orders[lastI] != 0 && index > lastI {
				fromOrder = p.getOrder(orders[lastI])
				loadedNextOrders := p.loadSellOrders(p, fromOrder, index-lastI)
				// тк нет грязных, то просто складываем
				orders = append(orders, loadedNextOrders...)
				// тут нужно выйти и отдать то что есть
				// todo
			}
			// тут нужно выйти и отдать то что есть
			// todo
		}
	} else {
		orders = p.loadSellOrders(p, nil, index+1)
		if p.hasUnsortedSellOrders() || p.hasDeletedSellOrders() {
			orders, _ = p.updateDirtyOrders(orders, true)
		}
		// если пусто, то загрузить столько сколько нужно
		//if orders[len(orders)-1] != 0 {

		//}
		// можно не сортировать
	}
	//log.Println("orders end", orders)
	p.setSellOrders(orders)

	i := len(orders) - 1
	if i >= 0 && orders[i] == 0 {
		i--
	}
	if i < index {
		return nil
	}

	order := p.getOrder(orders[index])

	return order
}

func (p *Pair) OrdersSell(limit uint32) []*Limit {
	p.lockOrders.Lock()
	defer p.lockOrders.Unlock()
	return p.ordersSell(limit)
}

func (p *Pair) ordersSell(limit uint32) []*Limit {
	index := int(limit - 1)
	p.orderSellByIndex(index)
	orderIDs := p.SellOrderIDs()
	if len(orderIDs) > index {
		return p.getOrders(orderIDs)[:index+1]
	}
	return p.getOrders(orderIDs)
}

func (p *Pair) OrderSellLast() (limit *Limit, index int) {
	p.lockOrders.Lock()
	defer p.lockOrders.Unlock()

	return p.orderSellLast()
}

func (p *Pair) orderSellLast() (limit *Limit, index int) {
	for order := p.orderSellByIndex(index); order != nil; order = p.orderSellByIndex(index) {
		limit = order
		index++
	}
	return limit, index - 1
}

func (p *Pair) AddLastSwapStepWithOrders(amount0In, amount1Out *big.Int, buy bool) EditableChecker {
	if amount0In.Sign() == -1 || amount1Out.Sign() == -1 {
		return p.reverse().AddLastSwapStepWithOrders(big.NewInt(0).Neg(amount1Out), big.NewInt(0).Neg(amount0In), !buy).Reverse()
	}

	var orders []*Limit
	if buy {
		amount0InCalc, ordrs := p.calculateSellForBuyWithOrders(amount1Out)
		if amount0InCalc.Cmp(amount0In) != 0 {
			log.Println("AddLastSwapStepWithOrders calculateSellForBuyWithOrders error", amount0InCalc, amount0In)
		}
		orders = ordrs
	} else {
		amount1OutCalc, ordrs := p.calculateBuyForSellWithOrders(amount0In)
		if amount1OutCalc.Cmp(amount1Out) != 0 {
			log.Println("AddLastSwapStepWithOrders calculateBuyForSellWithOrders error", amount1OutCalc, amount1Out)
		}
		orders = ordrs
	}

	reserve0, reserve1 := p.Reserves()

	ordrs := make(map[uint32]*Limit, len(p.orders.list))
	dirtyOrdrs := make(map[uint32]struct{}, len(p.dirtyOrders.list))
	p.lockOrders.Lock()
	for k, v := range p.orders.list {
		if v == nil {
			ordrs[k] = nil
			continue
		}
		ordrs[k] = v.clone()
	}
	for k, v := range p.dirtyOrders.list {
		dirtyOrdrs[k] = v
	}
	p.lockOrders.Unlock()

	unsortedDirtySellOrders := map[uint32]struct{}{}
	p.unsortedDirtySellOrders.mu.Lock()
	for k, v := range p.unsortedDirtySellOrders.list {
		unsortedDirtySellOrders[k] = v
	}
	p.unsortedDirtySellOrders.mu.Unlock()
	unsortedDirtyBuyOrders := map[uint32]struct{}{}
	p.unsortedDirtyBuyOrders.mu.Lock()
	for k, v := range p.unsortedDirtyBuyOrders.list {
		unsortedDirtyBuyOrders[k] = v
	}
	p.unsortedDirtyBuyOrders.mu.Unlock()
	deletedSellOrders := map[uint32]struct{}{}
	p.deletedSellOrders.mu.Lock()
	for k, v := range p.deletedSellOrders.list {
		deletedSellOrders[k] = v
	}
	p.deletedSellOrders.mu.Unlock()

	deletedBuyOrders := map[uint32]struct{}{}
	p.deletedBuyOrders.mu.Lock()
	for k, v := range p.deletedBuyOrders.list {
		deletedBuyOrders[k] = v
	}
	p.deletedBuyOrders.mu.Unlock()

	pair := &Pair{
		lockOrders: &sync.RWMutex{},
		PairKey:    p.PairKey,
		pairData: &pairData{
			RWMutex:   &sync.RWMutex{},
			Reserve0:  reserve0,
			Reserve1:  reserve1,
			ID:        p.ID,
			markDirty: func() {},
		},
		sellOrders: &limits{
			ids: p.sellOrders.ids[:],
		},
		buyOrders: &limits{
			ids: p.buyOrders.ids[:],
		},
		orders: &orderList{
			mu:   sync.RWMutex{},
			list: ordrs,
		},
		dirtyOrders: &orderDirties{
			mu:   sync.RWMutex{},
			list: dirtyOrdrs,
		},
		deletedSellOrders: &orderDirties{
			mu:   sync.RWMutex{},
			list: deletedSellOrders,
		},
		deletedBuyOrders: &orderDirties{
			mu:   sync.RWMutex{},
			list: deletedBuyOrders,
		},
		markDirtyOrders: p.markDirtyOrders,
		loadBuyOrders:   p.loadBuyOrders,
		loadSellOrders:  p.loadSellOrders,
		loadedSellOrders: &limits{
			ids: p.loadedSellOrders.ids[:],
		},
		loadedBuyOrders: &limits{
			ids: p.loadedBuyOrders.ids[:],
		},
		unsortedDirtyBuyOrders: &orderDirties{
			mu:   sync.RWMutex{},
			list: unsortedDirtyBuyOrders,
		},
		unsortedDirtySellOrders: &orderDirties{
			mu:   sync.RWMutex{},
			list: unsortedDirtySellOrders,
		},
		getLastTotalOrderID: nil,
		loadOrder:           p.loadOrder,
	}
	commission0orders, commission1orders, amount0, amount1, _ := CalcDiffPool(amount0In, amount1Out, orders)

	pair.lockOrders.Lock()
	defer pair.lockOrders.Unlock()

	if amount0.Sign() != 0 || amount1.Sign() != 0 {
		pair.update(amount0, big.NewInt(0).Neg(amount1))
	}

	pair.update(commission0orders, commission1orders)

	oo := make([]*Limit, 0, len(orders))
	for _, order := range orders {
		oo = append(oo, &Limit{
			IsBuy:        order.IsBuy,
			WantBuy:      big.NewInt(0).Set(order.WantBuy),
			WantSell:     big.NewInt(0).Set(order.WantSell),
			Owner:        order.Owner,
			PairKey:      order.PairKey,
			oldSortPrice: big.NewFloat(0).Set(order.oldSortPrice),
			id:           order.id,
			RWMutex:      &sync.RWMutex{},
		})
	}

	pair.updateOrders(oo)

	pair.orderSellByIndex(0)

	return pair
}
