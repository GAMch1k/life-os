package recipe

import "testing"

func TestIngredientLinesSplitIntoAmountAndName(t *testing.T) {
	for line, want := range map[string]Ingredient{
		"200 g Mehl":                {Amount: "200 g", Name: "Mehl"},
		"500g Hackfleisch":          {Amount: "500g", Name: "Hackfleisch"},
		"2 EL Olivenöl":             {Amount: "2 EL", Name: "Olivenöl"},
		"1 Prise Salz":              {Amount: "1 Prise", Name: "Salz"},
		"1/2 TL Zimt":               {Amount: "1/2 TL", Name: "Zimt"},
		"½ Bund Petersilie":         {Amount: "½ Bund", Name: "Petersilie"},
		"1 ½ cups flour":            {Amount: "1 ½ cups", Name: "flour"},
		"2 große Zwiebeln":          {Amount: "2", Name: "große Zwiebeln"},
		"2-3 Knoblauchzehen":        {Amount: "2-3", Name: "Knoblauchzehen"},
		"1,5 l Milch":               {Amount: "1,5 l", Name: "Milch"},
		"1 ст. л. сахара":           {Amount: "1 ст. л.", Name: "сахара"},
		"2 ст.л. муки":              {Amount: "2 ст.л.", Name: "муки"},
		"300 г творога":             {Amount: "300 г", Name: "творога"},
		"3 яйца":                    {Amount: "3", Name: "яйца"},
		"Salz und Pfeffer":          {Name: "Salz und Pfeffer"},
		"Olivenöl zum Braten":       {Name: "Olivenöl zum Braten"},
		"  2   tbsp   &amp; butter": {Amount: "2 tbsp", Name: "& butter"},
		// A line that is only an amount stays the name.
		"2 EL": {Name: "2 EL"},
		"500":  {Name: "500"},
	} {
		if got := SplitIngredient(line); got != want {
			t.Errorf("SplitIngredient(%q) = %+v, want %+v", line, got, want)
		}
	}
}
