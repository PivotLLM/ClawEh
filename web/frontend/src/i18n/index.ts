import dayjs from "dayjs"
import "dayjs/locale/en"
import localizedFormat from "dayjs/plugin/localizedFormat"
import relativeTime from "dayjs/plugin/relativeTime"
import i18n from "i18next"
import { getDefaultStore } from "jotai"
import { initReactI18next } from "react-i18next"

import { productNameVariables, productNamesAtom } from "@/store/product"

import en from "./locales/en.json"

dayjs.extend(relativeTime)
dayjs.extend(localizedFormat)
dayjs.locale("en")

// productNamesChanged is the i18next event that re-renders translated
// components after the product names change (store/product).
export const productNamesChanged = "productNamesChanged"

i18n
  // pass the i18n instance to react-i18next.
  .use(initReactI18next)
  // init i18next
  // for all options read: https://www.i18next.com/overview/configuration-options
  .init({
    resources: {
      en: {
        translation: en,
      },
    },
    lng: "en",
    fallbackLng: "en",
    debug: false,

    interpolation: {
      escapeValue: false, // not needed for react as it escapes by default
      // {{name}}, {{shortName}} and {{binaryName}} in any string: the
      // product's names from the server (store/product).
      defaultVariables: productNameVariables,
    },
    // Re-render translated components when the names change, not only on a
    // language change: their sentences read the names through
    // defaultVariables above.
    react: {
      bindI18n: `languageChanged ${productNamesChanged}`,
    },
  })

getDefaultStore().sub(productNamesAtom, () => {
  i18n.emit(productNamesChanged)
})

export default i18n
