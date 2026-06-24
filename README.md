# payment-paytabs

[PayTabs](https://support.paytabs.com/en/support/home) driver for togo **payment**.

```bash
togo install togo-framework/payment
togo install togo-framework/payment-paytabs
```
```env
PAYMENT_DRIVER=paytabs
PAYTABS_SERVER_KEY=...
```

Registers on the togo `payment.PaymentProvider` interface and is selected via
`PAYMENT_DRIVER=paytabs`. Gateway API calls are scaffolded — see the PayTabs docs.

MIT
